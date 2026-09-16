# 安全方案实施与本地验收

> v0.1.2 发行更新：默认采用官方 HTTPS 与 SHA256，不要求发布签名密钥、独立信任根或元数据服务。本文中的强制签名、签名撤销和安全代次要求属于此前设计或可选签名模式，不代表默认发行版保证。最终安装与迁移方式以 [README](../README.md) 和 [迁移说明](security-migration.md) 为准。

后续第二轮的实施状态单独记录在 [加固进度](security-hardening-v2-progress.md)，下文是第一轮历史基线。

## 1. 验收范围

日期：2026-09-16。下列本轮本地检查均已通过。对象为当前工作树的 S01–S08 安全实现，基线提交 `c85c5ac`，不是已发布版本。测试只使用临时目录、合成数据、真实 Chrome 和一次性 Linux arm64 容器；Linux amd64 已交叉编译，未在 amd64 宿主执行本轮容器场景；没有连接或修改真实 VPS，没有提交、推送或发布。

执行边界采用“非 root 网络子进程 + 固定 root 协调执行”，不是整个 agent 非 root。发布信任使用 go-tuf/v2，生产签名密钥、独立初始根交付和线上元数据服务需要维护者另行配置。本轮不能替代独立渗透测试或真实部署 canary。

## 2. 实施结果对照

| 工作包 | 已落地内容 | 主要证据 |
|---|---|---|
| S01 | 显式路由角色、完整 Origin/CSRF、Secure host cookie、Host/代理边界、成员资源过滤、SSE 权限重验 | `internal/api/security_test.go`、认证/维护/分享测试、真实浏览器脚本 |
| S02 | HTTPS 默认、精确 HTTP/私网来源例外、实际拨号 IP 固定、混合 DNS 结果拒绝、每跳重验、禁代理环境、响应与并发上限 | `internal/safehttp/client_test.go`、订阅测试 |
| S03 | Argon2 参数/并发预算、公共入口查库前限流、SSE 与 gzip 预算、请求超时和集合上限 | auth/API/connlog 测试、真实控制端压力场景 |
| S04 | 头像解码重编码、二维码惰性图片、CSP、订阅参数白名单、语境转义、强短码、解析和渲染预算 | 浏览器、头像、订阅和 proxynode 测试及 fuzz |
| S05 | 注册原子单次消费、设备绑定配置/摘要/代次、日志批次事务去重、第二因素 CAS、账号安全版本撤销会话 | store/API/agentproto/agent/connlog 测试及 race |
| S06 | agent/controller/core/安装器执行前独立验签、根轮换、时效/身份/代次验证、本机回退凭证、旧 agent 禁止自动升级 | secureupdate 测试、安装和真实 systemd 维护回归 |
| S07 | UID 65534 网络处理、固定本机能力、证书 ID/ACME 域名、节点/内存限制、代理目的地址隔离、systemd 限制 | root socket 策略负向测试、内核 nft 测试、真实 sing-box 计量、core 配置测试 |
| S08 | 秘密字段加密、独立密钥、加密一致性备份/恢复撤销、数据库增长上限、部署默认保护、CI 门禁与许可同步 | store/backup 测试、安装/卸载/维护/Docker、扫描和双架构构建 |

## 3. 可复现检查

### 3.1 代码、浏览器与静态检查

- `bash scripts/check.sh`：前端测试、typecheck/build、Go vet、全量 Go 测试、主机及 Linux amd64/arm64 编译、安装与卸载参数检查。
- 安全相关包 `go test -race`：auth、store、api、maintenance、agent、secureupdate、safehttp、backup、connlog。
- `go test ./internal/proxynode -run '^$' -fuzz FuzzParseAny -fuzztime 20s -parallel 2`：本轮 637,110 次执行通过，不能据此证明所有解析输入安全。
- `scripts/security-browser.mjs`：真实 Chrome、临时 HTTPS 站点、同站不同子域及跨站网页；正常初始化/管理写入成功，攻击无管理副作用，缺 CSRF 拒绝，内联脚本与 iframe 被阻止；外部证书 ID 部署入口可操作。
- ShellCheck v0.11.0、actionlint v1.7.12、`git diff --check`；gitleaks v8.30.1 工作树与 17 个历史提交扫描无秘密发现。
- `govulncheck v1.1.4`：应用可达漏洞 0、已导入包漏洞 0；依赖模块仍报告 1 项未导入的 `golang.org/x/crypto/openpgp` 问题（GO-2026-5932）。不是“全部依赖没有漏洞”。`npm audit` 为 0。
- 第三方许可清单重新生成，覆盖 192 项依赖。

### 3.2 隔离系统集成

本地 `make release VERSION=v0.0.0-security REPOSITORY=example/VpsCT` 仅生成测试附件。

| 命令 | 核验内容 |
|---|---|
| `scripts/test-install-container.sh release/v0.0.0-security` | 可信引导、安装/初始化、分发、重复安装拒绝、损坏包、升级、降级拒绝、失败回退；Caddy 配置保护与校验 |
| `scripts/test-maintenance-container.sh release/v0.0.0-security` | 10 项场景：真实非 root 控制端/root helper、socket 权限及本机能力拒绝、重启后任务继续、数据恢复、校验失败不换程序、压力下心跳、真实 agent 更新/回退、两端清空隔离 |
| `scripts/test-uninstall-container.sh` | 16 个真实 systemd/nft 场景，包含保留/清空、两端同机、重复执行、异常路径和停服失败 |
| `scripts/test-docker.sh` | 非 root、只读根文件系统、移除 capabilities、独立可写卷、重启后初始化能力保留 |
| `scripts/test-meter-container.sh` | 分别独立容器测试计费和出站边界，避免规则污染；IPv4/IPv6 TCP/UDP 计费语义保持 |

额外使用官方 [sing-box v1.12.14](https://github.com/SagerNet/sing-box/releases/tag/v1.12.14) 的 Linux arm64 资产，校验官方 API 提供的 SHA256 后，仅在隔离容器验证真实代理计量和阻断。Snell 目的地址隔离通过实际 cgroup socket 测试，未宣称本轮已覆盖每个 Snell 发布版本的端到端客户端组合。

### 3.3 负载与恢复边界

真实 systemd 控制端限制 512 MiB。合成 40 个并发密码请求中 38 个被限流，合法心跳约 3 毫秒，控制端峰值 RSS 约 239 MiB。该场景验证预算和隔离，不是公网 DDoS 容量认证。

备份测试覆盖密文完整性、错误密钥、截断、秘密字段迁移、恢复后会话及注册能力清理；维护测试覆盖新程序启动失败后的旧程序/数据恢复。断电依靠持久任务状态、不自动重放破坏性任务；本轮未模拟真实磁盘控制器断电。数据库上限避免无限增长，但仍需运维监测可用磁盘。

## 4. 发布前仍需维护者决策的事项

1. 是否合并/提交本次变更，并确定新的安全版本和安全代次。
2. 谁保管离线根、受保护签名环境、内核目录及元数据续期；如何独立分发可信验证器。
3. 选择一台明确授权的 canary，核对现有 HTTP/私网订阅、旧短码、证书及代理私网路由，再决定迁移批次。
4. 生产恢复演练和供应商网络防护。当前日常备份周期不构成生产 RPO/RTO SLA。

以上属于发布与部署阶段，不在本次本地测试中自动执行。配置方法见 [安全迁移](security-migration.md)，原始要求见 [安全设计](security-design.md)。
