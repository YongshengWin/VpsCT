# 1. UDP 固定转发验证

## 1.1 实现方式

当前源码支持固定目标的 UDP 和 TCP+UDP 转发，不需要 realm。数据路径仍使用官方 sing-box；agent 负责补齐空闲会话回收，不修改或自行编译内核。需要同时更新控制端与 agent，并在设置里保存官方 sing-box 1.14.1 或兼容的稳定 1.14.x；不会自动改变已有内核设置。旧 agent 的启用预检与配置获取会被拒绝；使用过 UDP 的 agent 状态会阻止降级到缺少该能力的版本。

仅有活动 UDP 转发时，共享代理进程启用带随机本机密钥的 `127.0.0.1` 临时 API。root agent 按服务 PID 与 socket inode 找到监听器，小规模每轮完成后等待 500 毫秒、大规模等待一秒，只读查询带固定转发 mark 的内核 conntrack，再读取完整的 API 会话快照。仅关闭已失去内核准入、连续两次观察无活动、字节计数不再变化且达到配置空闲时长的固定转发 UDP UUID。代理节点 UDP、TCP 和其他转发不由该逻辑关闭。计量继续由原有 nft 规则负责，不叠加 API 字节数。

每条转发的 UDP 上限为 1–1024，整台 agent 所有启用 UDP 转发的配置预算合计不超过 8192。空闲时长为 2–600 秒；实际关闭包含轮询与保守确认延迟。API 不完整、conntrack 无法读取等连续故障会停止独立 UDP 健康租约续期并撤销准入，只关闭固定转发 UDP 通路；TCP、普通代理节点和共享隧道不因此停服。恢复时先清理残留固定转发 UDP 会话，再开放 UDP。静态规则由既有 watchdog 核验修复，重建后租约为空；网络监测器不能代替 UDP 会话工作线程续期。

## 1.2 适用范围

支持 IPv4/IPv6 固定目标、直连出口、已开启 UDP 的 SOCKS5 和 WireGuard 出口。SSH 出口仅支持 TCP。私网 UDP 固定目标尚未开放，不能用节点的私网业务访问开关代替转发授权。

官方 sing-box direct 入站处理零负载 UDP 报文时可能关闭或重建映射。内核会记录该报文的准入，但不能保证这类空报文透传或仅靠空报文保活；后续普通报文可重新建立可用映射。依赖零负载报文语义的应用不在当前支持范围。

官方 sing-box 1.14.1 的 TCP 半关闭检查未通过，依赖发送 EOF 后继续接收响应的应用须单独验证。

## 1.3 验证与复现

验证全部在临时隔离容器进行，没有连接或修改真实 VPS。基线为官方 sing-box 1.14.1 ARM64 普通构建与 musl 构建。完整控制端 → agent → 服务路径覆盖持续请求、仅下行持续流量、零负载报文后的普通请求恢复、空闲 API 会话及实际出站 socket 消失、同客户端重新建会话、会话管理故障暂停和恢复、独立计量、端口修改、删除后的最终结算。

```sh
NETWORK_TEST_ARCH=arm64 SINGBOX_BIN=/path/to/official/sing-box \
  NETWORK_SYSTEMD_CASE=forward-udp bash scripts/test-network-systemd-container.sh
ALPINE_TEST_ARCH=arm64 SINGBOX_MUSL_BIN=/path/to/official/sing-box-musl \
  ALPINE_TEST_CASE=udp ALPINE_TEST_MEMORY=512m bash scripts/test-alpine-container.sh
```

组合场景 `forward-both`、`forward-udp6`、`forward-socks-udp`、`forward-wireguard-udp` 已在 ARM64 通过相同控制端/agent 生命周期。WireGuard 的业务 UDP 会话位于用户态隧道内，回收后保留共享隧道 socket，不按每个业务会话要求新增系统 socket。原生 AMD64/ARM64 CI 已加入这些场景和 Alpine UDP；本地交叉编译不等于原生 AMD64 流量验收，未运行的远端 CI 不计为通过。

## 1.4 回收方案依据

未启用 agent 回收时，官方 sing-box 1.14.1 和 1.14.3 在入站及路由均配置两秒 UDP 超时后，空闲五秒仍保留出站 socket；启用 `udp_connect` 也未通过。检查先读取内核 socket 表，再发送下一报文，避免仅比较源端口造成重用误判。

官方 [direct 入站实现](https://github.com/SagerNet/sing-box/blob/v1.14.3/protocol/direct/inbound.go) 使用 UDP NAT 缓存；其依赖的 [NAT 连接](https://github.com/SagerNet/sing/blob/0ad23b637bd4/common/udpnat2/conn.go) 与 [缓存刷新](https://github.com/SagerNet/sing/blob/0ad23b637bd4/contrab/freelru/lru.go) 是调查线索。当前方案通过官方本地管理接口回收连接，保留官方发行二进制；这不代表上游已经修复缓存行为。

## 1.5 本地交付状态

初次 UDP 支持的本地候选 `release/v0.1.999-issues.4` 包含第一版回收逻辑，未包含随后这轮必要优化；早先 `.3` 不包含。候选双架构附件的 SHA256 与归档内控制端、两种 agent、版本及当前说明已核对；ARM64 附件 agent 的 `capabilities` 实测声明 `forward_udp: 1`。没有 commit、push、签名、发布或真实 VPS 操作。

完整 `scripts/check.sh` 通过，包括前端 44 项测试、typecheck/build、Go vet/test、agent API 契约、Python 检查、Linux 双架构构建和安装卸载参数检查。最终可见限制提示修改后重新 typecheck/build，并用 Chrome 点验 UDP/TCP+UDP 选择、预算字段、移动布局、Cookie/CSRF/CSP。会话回收及 agent 的 race 检查通过，ShellCheck、actionlint 与 diff 空白检查通过。官方 1.14.3 的隔离转发矩阵使用最终回收逻辑，通过真实空闲 socket 关闭、并发预算回收、来源撤销、停用、目标替换及独立资源计量；它不代替 1.14.3 全量控制端与 Alpine 生命周期验收。

## 1.6 必要可靠性优化

第二版 UDP 能力要求独立健康租约与运行诊断，旧版能力不能取得启用 UDP 的配置；使用第二版后拒绝降级到第一版 agent。每条入站的 1024 上限来自官方 [UDP NAT 实现](https://github.com/SagerNet/sing/blob/0ad23b637bd4/common/udpnat2/service.go)，超量配置已在真实压测中复现活跃映射被缓存替换，不能继续把旧 4096 上限当作稳定容量。旧记录仍能读取、停用和清理。整机预算仍为 8192，需分布在多条转发中。详见 [必要优化迭代记录](udp-reliability-iteration.md)。

8192 是配置预算的安全上限，并非承载能力保证。默认 256 MiB 代理预算的发布验收基线为 1024 个持续活跃 UDP 会话；原生 AMD64 的 8192 会话强负载尚未通过，会触发 API 不可用保护。请按资源和实际负载控制并发，不能将其换算为固定用户数。
