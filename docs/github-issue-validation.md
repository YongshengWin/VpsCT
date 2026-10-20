# GitHub Issue 修复与验证记录

本文保留各阶段的历史验收记录。2026-10-21 起发行范围收敛为 Linux AMD64：ARM 构建、CI 矩阵、安装与升级支持暂停，历史独立 ARM Release 附件已撤下。下文双架构或 ARM 记录不代表当前支持，现行要求见 [运维说明](operations.md)。

## 1. 范围与状态

2026-10-15，按 [迭代方案](github-issue-iteration-plan.md) 实施当前 5 条 issue 对应的修复与收尾。基于现有未提交工作区增量修改；没有 commit、push、修改 issue 状态、发布 Release 或连接真实 VPS。源码修复、运行验收与发布完成分别记录，当前不能宣称所有退出条件已完成。

本地测试候选版本为 `v0.1.999-issues`，仅用于区分历史正式版本与当前测试制品，不是正式版本。已有 Alpine、多网卡和隧道实现沿用当前源码，不重复开发第二套服务或网络模型。

后续继续优化使用独立本地候选 `v0.1.999-issues.2`；新增内容与验证见第 6 节，第一轮历史验证记录保留其原范围。

再次续作修复 Alpine 首次安装的 noexec 临时目录与安装锁继承问题，对应本地候选 `v0.1.999-issues.3`，详见第 6.5 节。

### 1.1 Issue 对照

| Issue | 本次交付 | 状态与边界 |
|---|---|---|
| [#32](https://github.com/YongshengWin/VpsCT/issues/32) | 服务只生成一个最终 Slice；核对服务与目标 Slice 的精确 cgroup 父子关系，并核对 systemd MainPID 的实际成员身份；保留最小权限；SO_MARK/seccomp 失败给出可识别原因；旧日志恢复专用账号与 0600 权限 | ARM64 真实 systemd、OpenRC 与代理连接通过。未给所有代理增加 NET_ADMIN；旧内核/LXC 能力不足仍明确拒绝 |
| [#33](https://github.com/YongshengWin/VpsCT/issues/33) | 支持可证明目的端口不相交的原生 nft fail2ban 和直接端口规则；原有 xt multiport 交叉核对保留 | 单元反例与真实 nft 流量通过；源端口、默认拒绝、未知表达式、动态端口集合仍拒绝 |
| [#26](https://github.com/YongshengWin/VpsCT/issues/26) | 验收已有 Alpine sing-box、mieru、维护生命周期；修复首次安装 noexec 与安装锁继承；增加真实安装、终端更新和原生双架构 CI | ARM64 运行与真实安装更新通过；本机 AMD64 仿真 seccomp 不可用，原生 AMD64 CI 尚未运行 |
| [#17](https://github.com/YongshengWin/VpsCT/issues/17) | 验收已有多网卡、SSH/WireGuard 出口与计量，增加完整使用示例和 CI | ARM64 direct、SSH、WireGuard 实际链路通过；不扩展为整机 VPN 或所有协议任意组合 |
| [#16](https://github.com/YongshengWin/VpsCT/issues/16) | 复用 Host 信任边界测试与 Chrome 安全回归 | 保留已关闭状态，不关闭 Host 校验 |

## 2. 行为与安全边界

### 2.1 代理隔离

`ServiceUnit` 在输出前解析最终 Slice，只生成一个值；预检的 `system.slice` 与运行服务的权限组各自保持一致。运行服务必须位于目标 Slice 的直接子 cgroup，systemd 下 MainPID 还必须实际位于该服务组；不能凭路径包含某个名字认定归属。OpenRC 继续使用自己的受管服务校验。

当前 sing-box 配置会生成路由 mark，因此仍探测实际 SO_MARK 操作。按需授予 CAP_NET_RAW，运行态允许集保持一致；Linux 5.17 起该权限可用于 SO_MARK，旧内核或受限运行环境需要通过实际能力检查。没有以扩大 NET_ADMIN、取消 seccomp 或放松网络保护处理不兼容环境。

日志父目录仍由 root 管理，只修复具体日志文件的账号和 0600 权限。Linux 夹具预置 root 所有且权限为 000 的旧日志，验证实际启动路径恢复写入。

### 2.2 防火墙兼容

新增分析只处理 ip/ip6/inet 默认允许的外部 input 链；每条规则均须存在可证明不影响受管 TCP/UDP 目的端口的约束。支持等值、内联集合和闭区间，以及只读地址匹配与计数器；只在端口证明之后接受受支持的终结动作。默认拒绝、仅源端口、否定匹配、未知表达式、提前跳转或修改、未解析的端口集合都保守失败。

真实测试验证节点端口可连接、外部保护端口仍阻断、重叠拒绝、重复应用幂等及删除仅清理 VpsCT 规则。自动放行成功不代表云安全组或公网链路已可用。

### 2.3 错误与恢复

沿用现有配置结果和界面错误展示，新增稳定标识：`proxy_mark_unavailable`、`proxy_sandbox_unavailable`、`proxy_cgroup_mismatch`、`ingress_policy_unsupported`。agent 日志增加错误码字段。

相同修订、hash 和完整错误每五分钟最多重复记录一次；错误或修订变化立即记录，成功后重置。限频只作用于日志，配置收敛与结果上报仍每轮执行，环境恢复后可以重新应用。没有增加数据库迁移。

## 3. 已执行验证

### 3.1 开发与浏览器

1. 第一轮完整 `scripts/check.sh` 通过：前端测试、typecheck/build、Go vet/test、agent API 契约、Python 客户端检查、Linux 双架构构建及安装卸载参数测试。最终修改后前端、vet、契约与 Python 检查重跑通过；并发 Go 重跑在测试子进程停留较久，改用 `go test -p 1 ./... -count=1 -timeout=3m` 后全仓库通过，不能把原并发重跑记为一次完整成功。
2. `core`、`nft`、`openrc`、`proxysandbox` 针对性测试通过，新增目的端口反例、Slice、cgroup 边界、最小权限和日志聚合测试。
3. Chrome 使用临时空数据库与 HTTPS 测试域名，通过初始化、服务器创建、维护横幅、网络导航、UDP 与 TCP+UDP 固定转发选择及预算字段、390px 布局以及 Cookie/CSRF/CSP 检查。
4. 浏览器脚本原有网卡入口名称过期，已对照当前“网卡与流量”入口修正并重跑通过。

### 3.2 ARM64 真实 Linux

| 验证 | 环境与结果 |
|---|---|
| Slice 与防火墙回归 | 一次性 systemd 容器，实际 Slice/cgroup、nft 规则与网络连接、seccomp 线程和 socket 探测通过 |
| 多网卡 direct | 实际控制端/agent/helper/systemd，TCP/UDP、IPv4/IPv6、DNS、双分享账本、维护锁、出口停用恢复、控制端离线、网卡消失恢复、agent 重启身份失效、显式重新绑定、网卡改名、分享停用恢复、删除收敛通过 |
| SSH | 独立 Shadowsocks 客户端、固定主机公钥、选定外层接口、TCP 与业务 DNS、分享计量通过；UDP 拒绝 |
| WireGuard | 独立 Shadowsocks 客户端、独立 Peer 身份、选定接口、TCP/UDP、业务 DNS与分享计量通过 |
| Alpine sing-box | Alpine 3.23、官方 sing-box 1.14.1 musl，256/512 MiB 服务、helper 恢复、计量、NAT、配额与隔离通过，无 OOM；带旧日志权限用例的 512 MiB 最终运行也通过 |
| Alpine mieru | 官方 mita/mieru 3.37.0、512 MiB，TCP/UDP、cgroup-skb 双向计量、服务/helper 重启连续性、阻断与退役结算通过 |
| OpenRC 网页维护 | 实际启动就绪、升级成功/失败回滚、任务恢复、默认保留与显式清空数据卸载通过；本地候选校验使用明确的测试替身 |
| systemd 维护 | 12 场景通过，包含独立 root worker、二次授权、恢复文件被改后拒绝、控制端升级与旧程序/旧数据回滚、摘要失败、agent 升级与失败回滚、两端分离卸载及清空终态 |
| 官方 v0.1.6 历史升级 | 已核对下载附件摘要，离线经网页升级到候选；控制端先升级且旧 agent 离线、agent 随后升级、上游 latest 改变后的固定目标重装均保留订阅及真实域名 HTTP/HTTPS 流量 |

sing-box 与独立客户端的 direct 路径均通过；独立客户端不代表 sing-box 的所有 SS2022 客户端行为均已获验证。测试中的临时阻断产生预期连接错误，以最终断言及进程退出码判断结果。

### 3.3 架构与发行信任

官方 sing-box、mita/mieru 与 shadowsocks-rust 的归档对照 GitHub 官方 Release API 的 SHA256 校验，解包时只提取指定的普通文件。复现入口为 `scripts/fetch-network-test-fixtures.py DEST --arch arm64|amd64`，不存 GitHub token。

本机 AMD64 普通容器程序可以运行，但 seccomp 安装返回 EINVAL，独立最小沙箱用例也失败；Alpine 与 systemd AMD64 仿真结果不计为通过。新增原生 Ubuntu AMD64 CI 回归，包括防火墙、cgroup、seccomp、多网卡、SSH/WireGuard、Alpine 双内存档、mieru 和两种卸载。工作流通过静态校验，未推送，不能宣称远程 CI 已通过。

## 4. 复现与使用

1. `bash scripts/check.sh` 完成常规检查。
2. `bash scripts/test-issue-regressions-container.sh` 完成独立 systemd/nft/seccomp 回归，要求 Docker 可运行本机原生架构。
3. 使用 `scripts/fetch-network-test-fixtures.py` 获取官方测试内核，再按 `scripts/test-network-systemd-container.sh` 的 direct、ssh、wireguard 场景执行。SSH/WireGuard 使用 `NETWORK_TEST_CLIENT=shadowsocks-rust` 与已校验的 `SSLOCAL_BIN`。
4. 按 [Alpine 夹具说明](../scripts/alpine-test/README.md) 运行 full、mita 与 maintenance，包括显式 purge 的独立容器。脚本只操作一次性容器。
5. 安装与故障诊断见 [运维说明](operations.md)，多网卡操作示例见 [网络指南](network.md#34-多网卡与-ssh-出口示例)。

## 5. 最终交付检查

最终 Go 全仓库串行测试、前端检查与构建、API 契约、Python 检查、Chrome、systemd/OpenRC 候选维护矩阵与官方 v0.1.6 历史升级均已通过。候选包已在本地双架构构建，附件 SHA256 全部一致；ShellCheck、actionlint、脚本语法和文档本地文件链接检查通过。正式版本、原生 AMD64 CI、公开发行下载和真实目标试运行尚未执行，发布前必须补齐对应证据。历史升级仅替换隔离容器内的发行传输，保留默认摘要、HTTPS 和固定版本验证；不等同于候选包已公开发布。

维护夹具发现 systemd 容器继承 umask 0000，使测试安全策略目录与文件可写，产品校验器正确拒绝。已在夹具设置安全 umask、增加初始制品校验诊断，并等待 systemd 完整启动，不放宽产品校验；修正后完整 12 场景通过。

## 6. 继续优化补齐的范围

### 6.1 预检诊断的实际遗漏

第一轮按复用 unit 名称读取 journal，可能误读前一次启动失败；同时 `-p warning` 会漏掉 journald 未标为 warning/error 的普通 stderr。现在在启动前后核对 InvocationID，仅查询新的有效 InvocationID，并取消优先级筛选。旧 ID、缺失 ID、零 ID、无效 ID 或读取失败都不给出从历史日志猜测的原因。

systemd 与 OpenRC 共用脱敏诊断转换：已知 SO_MARK/seccomp 标识给出固定原因，其他失败统一为 `proxy_preflight_failed`，原始内核输出保留在本机，不进入控制端应用报告。上下文取消仍返回取消结果。单元测试覆盖历史 ID、非法 ID、OpenRC 拒绝和原始配置不泄漏；真实 systemd 连续制造 SO_MARK、普通失败、seccomp 三次不同失败，均正确分类，未被历史记录误导。

### 6.2 防火墙实际流量矩阵

扩展后的 `TestKernelForeignIngress` 已在 ARM64 Linux 通过：IPv4 TCP fail2ban、iptables 直接规则、IPv6 TCP fail2ban、IPv4 UDP 范围、IPv6 UDP 内联集合，以及相同端口号不同传输协议。每个场景验证正常节点可达、外部保护端口不可达、重叠拒绝与仅清理自有规则；随后增加覆盖节点的外部规则，验证相同节点配置重新检查时拒绝，不能缓存旧的安全证明。

### 6.3 面板恢复指引与窄屏

五种稳定错误分别显示防火墙、路由能力、进程隔离、资源组与配置预检的恢复指引；稳定错误码优先于宽泛的 timeout/certificate 文本推断。离线、暂停、重试中与已同步状态保持原有优先级，错误详情和全量同步重试语义保留。窄屏下操作按钮独占一行，避免把说明挤成过窄的文本列。

前端共 44 项测试、typecheck 和 build 已通过；Chrome 实际通过五种错误指引、错误详情、明暗主题、390px 布局及离线/恢复优先级，并保留已有网络导航与安全回归。截图复核确认窄屏说明与按钮分行，没有横向溢出。

### 6.4 完整检查与原生 CI

`scripts/check.sh` 新增可选 `CTLVPS_TEST_PARALLELISM`，只接受正整数，并为包测试设置五分钟超时；默认仍使用 Go 的包并发设置。本轮 `CTLVPS_TEST_PARALLELISM=1 bash scripts/check.sh` 已从头到尾返回 OK，覆盖全仓库 Go、前端、契约、Python、双架构程序构建与安装卸载检查；最终补充改动的相关 Go 测试/vet、前端检查及 ARM64 Alpine 512 MiB 回归通过。

兼容 CI 从仅 AMD64 扩展为原生 AMD64/ARM64 两个 job，分别使用 `ubuntu-24.04` 和 `ubuntu-24.04-arm`，覆盖 systemd/cgroup/nft/seccomp、多网卡、SSH/WireGuard、Alpine 双内存档、mieru 与维护；架构标签依据 [GitHub 官方 runner 说明](https://docs.github.com/en/actions/reference/runners/github-hosted-runners)。actionlint 与 ShellCheck 通过，但尚未提交推送执行，不能把静态校验记为原生 CI 成功。

### 6.5 Alpine 真实首次安装与终端更新

新增 `scripts/test-alpine-installer-container.sh`，在独立 256 MiB ARM64 Alpine/OpenRC 容器执行未经改写的生产安装脚本，临时目录明确挂载 `noexec`。真实测试发现并修复两项遗漏：原先从下载临时目录执行 OpenRC 预检，遇到 noexec 会失败；OpenRC 后台服务可能继承安装锁文件描述符，使后续更新被误判为已有安装任务。现在从目标二进制目录暂存并预检，成功后原子替换；退出时显式释放安装锁，并清理未完成的暂存文件。

最终实际通过首次安装、HTTPS 下载及 SHA256 校验、一次性注册、开机启用、真实进程就绪、鉴权心跳、退出后重新获取安装锁、损坏更新拒绝且保留原进程、正常跨版本终端更新、注册身份保留、不重复注册及停服后就绪失效，容器未 OOM。计量 nonce 在启动时允许轮换，不把它误判为注册身份丢失。单元回归另外检查预检执行目录和预检失败后的暂存清理。

夹具只在容器信任临时 HTTPS 证书并把 GitHub 解析到回环镜像；APK 包在镜像构建时真实下载，运行时以已安装依赖执行实际 apk 命令。它未模拟服务或 agent，也没有放宽产品下载摘要、HTTPS 或权限检查；它不等于公开 GitHub 发行验收。该流程已纳入原生 AMD64/ARM64 兼容 CI，但远程 CI 仍未运行。复现范围见 [Alpine 安装夹具说明](../scripts/alpine-test/README.md#14-真实安装脚本与终端更新)。

## 7. 尚未完成的退出条件

1. 最终候选提交并推送后，实际运行双架构 CI；本机 AMD64 用户态仿真不能替代该验收。
2. 核对正式版本号与最终制品，再做正式发行渠道的附件验证；本地候选版本不是正式 Release。
3. 用户指定目标和部署范围后的单台试运行；本次没有真实 VPS 操作授权。

上述操作中的 commit、push 与 Release 受 [AGENTS.md](../AGENTS.md) 的明确限制：“不要擅自 commit / push 或发布 Release；用户说了再做。”当前工作区包含本轮修复依赖的已有未提交功能，提交范围须以完整候选源码为依据核对，不能仅提交本轮新增测试后宣称 CI 验证了同一候选。

## 8. UDP 固定转发续作

UDP 与 TCP+UDP 已在当前源码开放，补充本机认证 API、只读 conntrack 会话回收、能力协商、旧 agent 拦截和降级保护。ARM64 systemd 与 Alpine/OpenRC 官方 1.14.1 验证通过持续流量、仅下行保活、真实空闲 socket 回收、同客户端重建、管理故障暂停恢复、独立计量、端口编辑与退役结算。双协议监听、IPv6、SOCKS5 与 WireGuard UDP 出口的相同生命周期通过。详细条件、官方内核零负载报文限制和复现方式见 [UDP 固定转发验证](udp-forwarding-validation.md)。早先本地 `.3` 候选不含此项，不能用它验证 UDP。

## 9. UDP 必要可靠性优化

后续优化独立记录在 [UDP 可靠性迭代](udp-reliability-iteration.md)：故障隔离、压力测试、运行诊断与官方每入站容量修正。原生双架构 CI 已加入压力测试，未运行的远程 CI 不计为通过。
