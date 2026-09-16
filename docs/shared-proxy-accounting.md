# 1. 共享代理进程与节点流量

## 1.1 运行结构

每台 VPS 的 sing-box 使用一个 `ctlvps-singbox.service`，配置中每个节点保留自己的入口、端口、协议和凭据。新增入口不再新增 Go 运行时。完整候选配置通过 `sing-box check` 后才替换；配置未变化不重启，启动失败恢复上一份配置和服务。

官方 Snell 仍按独立凭据运行服务实例，不更换协议实现。所有代理实例共同进入 `ctlvps-proxy.slice`。`memory_max_mb` 现在限制整组代理的内存，而不只是单实例；默认总上限 256 MiB，MemoryHigh 为其 75%。它是保护上限，过低会导致 OOM，不能作为节省实际内存的证据。代理预算不包含 agent 和控制端。配置前必须核对真实负载和剩余内存。

每个 Snell 节点的 `ctlvps-proxy-n<ID>.slice` 保持运行，IPAccounting 计数不随子服务重启清零。连接日志缓冲默认最多 20,000 条，并有 8 MiB 字节预算；这只限制可丢弃的连接日志，不丢弃流量账务。

## 1.2 流量口径

- 服务器：沿用网卡接收、发送和两者之和，包含代理、SSH、更新、DNS 等。
- sing-box 节点：入口连接和带节点标记的目标连接，在本机 input/output 钩子分别计数。节点 ID 是账务标识，端口复用不会将历史转给新节点。回环流量不计入这一口径。
- Snell 节点：只读取节点 slice 的 IPAccounting，不叠加 nft 数据。
- 分享：所属节点的入站、出站增量；不乘二，不与服务器网卡汇总相加。
- 共享 DNS、证书等无法唯一归属的控制流量只在服务器用量中体现，不按比例编造节点用量。Reality 握手连接也带节点标记；Hysteria2 未认证访问使用本地固定 404，避免额外外站伪装请求。

单进程的 `MemoryCurrent` 是整组服务 cgroup 的内存指标，不能按入口平均后声称为各节点实际 RSS。

## 1.3 结算和迁移

控制端必须先更新，支持 `metering_version=1`，再更新 agent。

1. 停止旧代理服务，读取 systemd 保留的最后计数。
2. 将固定结算快照保存到 agent 状态文件并同步到磁盘；控制端将计数基线、节点/服务器/分享汇总、分享额度放入同一个数据库事务。
3. ACK 丢失时重发同一快照。按节点、来源、进程或内核代次分别留存基线，旧代次重放不重复入账。
4. 建立 nft 规则和 Snell slice，再启动新结构。规则替换保留命名计数器，sing-box 重启不清零节点用量。
5. 成功后禁用旧 sing-box 实例并删除已结算的旧 nft 表。失败时保留错误信息并尝试恢复可用服务。无法识别旧服务归属、读取最终计数或获得控制端确认时不继续迁移。

节点删除后保留不含凭据的账务身份，以便最后一段流量仍能计入原节点和原分享。历史数据不清空，之前没有采集的数据不补造。延迟上报按采样时间进入历史；属于旧账期的数据不扣当前账期额度。

## 1.4 页面

服务器详情的“服务器流量”卡片可以直接选择整台服务器或该服务器的节点，查看入站、出站、汇总和趋势，支持近 7、30、90 天。节点列表显示近 30 天用量，可按服务器过滤和按用量排序。未采集、加载失败和零流量应区别展示。

## 1.5 本地验证

- `bash scripts/check.sh`：前端测试、类型检查、构建，Go vet/test，Linux amd64/arm64 构建，安装卸载参数校验。
- `go test -race ./internal/agent ./internal/core ./internal/traffic ./internal/conntail`：状态保存、迁移重试、账务事务、日志预算回归。
- `bash scripts/test-meter-container.sh`：一次性 Linux 网络命名空间中的合成 TCP/UDP、IPv4/IPv6；使用独立参考内核计数器核对每个方向的字节总数，验证节点隔离、规则替换与拦截。
- `SINGBOX_BIN=/absolute/path/to/verified/linux/sing-box bash scripts/test-meter-container.sh`：使用匹配 Docker 架构的官方 sing-box 做相同测试。报文应在协议 MTU 范围内；测试共传输 8 KiB UDP 负载，拆为 16 个 512 字节报文。
- `SINGBOX_BIN=/absolute/path/to/verified/linux/sing-box bash scripts/test-core-container.sh`：自动创建一次性 systemd 容器，验证下面的生命周期测试及 Snell 计数边界。
- `TestSharedServiceLifecycle`：只在 `CTLVPS_SYSTEMD_TEST=1` 的隔离 Linux systemd 容器启用；六入口单进程、幂等、无效配置不重启和监听冲突后恢复旧配置。
- `bash scripts/test-uninstall-container.sh`：真实 systemd/nft 卸载隔离验证，包含生成的 Snell slice 和精确路径白名单。
- `scripts/meter-test/measure-memory.py`：隔离环境空闲 PSS 基准。2026-09-16、Linux arm64、官方 sing-box 1.12.14、六个 Shadowsocks 入口：六进程合计 61,068 KiB，单进程 32,376 KiB，减少约 47%。这不是生产负载承诺。

## 1.6 上线边界

当前本地验证不能替代线上灰度：需要核验每台 VPS 的 nft/conntrack、IPAccounting、策略路由标记冲突及内存预算，备份控制端数据库和 agent 配置，先一台切换、逐协议重连并对账，再扩展。完整 IPv4/IPv6 的逐字节代理测试目前覆盖真实 Shadowsocks；其他协议通过完整配置校验，仍需客户端连通验证。

内核计数器不是掉电持久化账本。突然断电、手工清空 nft 表或停止计数 slice 可能丢失最后一次采样之后的字节；未观测区间不能恢复成“准确的零”。跨账期的采样增量归到采样结束时间，不能从单个累计值还原秒级跨月拆分。上述异常不能用估算掩盖，也不能把本地测试通过描述成所有线上流量已对账。

计数要求 Linux、nftables、conntrack、支持 IPAccounting 的 systemd；`0x43000000/0xff000000` 标记空间由 VpsCT 保留，冲突会中止应用。

Reality 拨号字段依据 [sing-box TLS 文档](https://sing-box.sagernet.org/configuration/shared/tls/#handshake)；本地 Hysteria2 伪装响应依据 [Hysteria2 文档](https://sing-box.sagernet.org/configuration/inbound/hysteria2/#masquerade)。实际兼容性以钉死版本的配置校验为准。

单机灰度可在 agent 的 systemd 启动参数加 `--hold-updates`，固定本机二进制，同时暂停自动同步和网页维护，避免控制端旧分发文件将灰度程序覆盖。此参数默认关闭。全量分发文件更新并通过验收后，移除该参数并重启 agent，恢复同步；不要在仍有维护任务运行时启用。

## 1.7 Snell 容量风险

本版 Snell 仍按独立节点运行官方服务端进程，未实现多用户单进程、Socket Activation 按需启动或空闲回收。节点数量和并发增长时，私有内存仍可能增加；操作系统共享代码页不能消除每个实例的运行时开销。没有经过压测，不能承诺低内存 VPS 能承载 100 个独立 Snell 节点。

代理 slice 的整体 MemoryMax 只是预算保护，达到上限可能 OOM 杀进程并中断连接，并不是内存优化本身。需要按实际负载评估容量；不要为了减少进程而把独立用户改成同一密码，否则会失去独立认证、撤销和准确归属。
