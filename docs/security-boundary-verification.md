# 代理权限与网络边界验证

> v0.1.2 发行更新：默认采用官方 HTTPS 与 SHA256，不要求发布签名密钥、独立信任根或元数据服务。本文中的强制签名、签名撤销和安全代次要求属于此前设计或可选签名模式，不代表默认发行版保证。最终安装与迁移方式以 [README](../README.md) 和 [迁移说明](security-migration.md) 为准。

## 1. 结论与范围

2026-09-16：在一次性、无外部网络的 Linux arm64 / systemd 容器中，复现两处隔离缺口。测试使用当前源码 `core.ServiceUnit` 与 `nft.EgressRules` 生成配置，不手写一套替代安全配置。运行内核为 `7.0.14-orbstack-00380-ga7e0a2dc9535`。

**前提是代理进程内已能执行攻击者代码。** 测试程序代替代理业务代码，在相同 UID、capabilities、systemd 文件系统保护与 cgroup 下运行。它验证失陷后的权限边界，不证明 sing-box/Snell 本身存在可远程触发的代码执行漏洞，也不证明普通代理请求或打开一个链接就能触发这些操作。

所有凭据文件、维护 socket 和目标服务都是合成夹具。没有读取真实凭据、连接真实 VPS 或访问云平台元数据服务。本次只新增验证脚本与记录，未修改产品安全策略。

## 2. 文件权限结果

两类代理单元均得到相同结果：实际 UID 为 0，NoNewPrivileges 为 1，有效 capabilities 为 `0x3400`（NET_BIND_SERVICE、NET_ADMIN、NET_RAW）。

| 合成对象 | 读取/连接 | 写入 |
|---|---|---|
| root:root 0700 目录下的 agent 状态文件（0600） | 允许 | 允许 |
| root:root 0700 目录下的信任状态文件（0600） | 允许 | 被只读挂载拒绝 |
| root:root 0700 目录下的维护状态文件（0600） | 允许 | 被只读挂载拒绝 |
| root 专有维护 socket 夹具 | 连接及夹具响应成功 | 未调用真实维护接口 |

负向对照：无关的 nobody 用户不能读取以上文件。因此结果不是因为夹具放宽了文件权限。ProtectSystem=strict 能阻止部分写入，却不会阻止同一 root 身份读取管理数据；agent 数据目录还在 ReadWritePaths 中。

## 3. 出站限制结果

建立本机回环、私网、仿元数据地址和文档地址段“公网分类目标”四类回显服务，全部位于隔离容器或其网络命名空间。每组同时验证 TCP 和 UDP，并要求收到回显，不能只依赖 UDP connect 返回成功。

| 代理权限与规则 | 本机/私网/仿元数据目标 | 公网分类目标 |
|---|---|---|
| sing-box，正确节点 mark | 拒绝 | 允许 |
| sing-box，mark=0 | **允许，绕过** | 允许 |
| sing-box，伪造未分配的 mark | **允许，绕过** | 允许 |
| sing-box，伪造已授权私网节点的 mark | **允许，绕过** | 允许 |
| Snell，以上四种 mark，实际节点 cgroup | 均拒绝 | 均允许 |

总计 64 项 TCP/UDP 回显探测符合上述结果。Snell 的 cgroup 匹配确实能抵抗改 mark。另做非破坏性权限探测：两类代理均能通过 nft 创建并删除一个空的测试表，证明仍保留修改防火墙的权限；没有删除或改写保护规则，也没有把这项结果称为已完成 Snell 规则绕过。

## 4. 复现与后续

运行 `GOCACHE=/tmp/ctlvps-security-go-cache bash scripts/test-security-boundary-container.sh`。运行器从生产函数生成夹具，在 `--network none` 的一次性 systemd 容器内测试，结束后删除容器与临时生成文件。脚本需要本地 Docker；禁止直接在真实 VPS 执行 Python 夹具。

生成器编译、ShellCheck、Python 语法检查与 git diff --check 通过。原始本地输出为 `/tmp/ctlvps-boundary-verification.log`。当前断言用于确认缺口存在；修复后应把对应允许结果改为拒绝结果，作为回归用例。

最小修复应围绕两件事：阻止代理访问管理数据；让网络限制依赖代理不能伪造的身份，并避免代理自己改规则。具体实现需同时验证证书、计量和正常连接，不能直接删除能力或目录访问后就宣布兼容。其他架构重构和生产密钥运维不属于本次验证范围。
