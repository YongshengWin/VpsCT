# VpsCT 安全迁移与可选签名配置

## 1. 适用范围

**v0.1.2 默认不需要发布签名密钥、信任根、签名元数据服务或预置验证器。** 安装器经 HTTPS 下载发行包及 SHA256SUMS，自动准备恢复工具；普通用户按 README 安装和升级即可。缺少本机策略时使用内置的权限与资源限制，节点默认不能访问私网。

后续签名章节仅适用于维护者主动配置 TUF 的自定义部署；默认版本不使用签名过期、撤销和安全代次机制。内核只接受固定官方 HTTPS 地址；配置了 SHA256 时必须匹配，未配置时依赖官方 HTTPS，不要求用户另建签名目录。HTTPS 与同源 SHA256 无法防住发布仓库同时被篡改。

本文对应安全加固后的源码，不能据此认为既有 Release 或真实 VPS 已升级。控制端与 agent 必须分批迁移；旧 agent 不会收到自动程序更新或网页维护任务。面板显示的安全状态来自 agent 上报，用于运维提示，不是远程证明。

正常浏览和配置操作不增加逐次认证；网页维护保留密码、已启用的第二因素和目标确认。本机策略是另一道执行边界，网页不能修改它。

## 2. 首次建立独立信任

### 2.1 发布者准备

在独立受控构建机上构建 `cmd/ctlvps-sign` 和 `cmd/ctlvps-verify`。核对源码提交、依赖及构建结果，通过独立可信渠道交付验证器、安装脚本和初始根公钥。不能把“从面板下载验证器”当成独立信任；新下载的程序也不能验证自己。

离线签名工具用法（路径均为占位符，不在控制端运行）：

```bash
ctlvps-sign --mode init --keys /offline/vpsct-keys
# 先在 keys/security-policy.json 中准备并审查累计策略，格式见下文。
ctlvps-sign --keys /offline/vpsct-keys --out /staging/metadata \
  --manifest /staging/signing-manifest.json --metadata-version 1
```

`init` 创建三把 root 密钥（`root-1.key` 至 `root-3.key`），根阈值为 2-of-3；targets、snapshot、timestamp 各使用独立密钥。三把根私钥应立即分开离线保管，初始化目录不能作为长期集中存储。常规发布不需要根私钥；轮换至少需要旧根与新根各自满足阈值。工具支持阈值不等于生产已部署多人审批或硬件密钥。根轮换：

```bash
ctlvps-sign --mode rotate --previous-keys /offline/old-keys --keys /offline/new-keys
```

轮换文件由新旧根共同签名，发布目录必须保留历代编号根。根默认一年，targets/snapshot 最多 31 天，timestamp 最多 7 天；在到期前重新签发并原子发布完整元数据，即使没有新版本也需要续期。元数据版本必须全局单调增加，不能重用或重置。过期会阻止新安装/更新，不主动终止现有代理。

`make release VERSION=vX.Y.Z REPOSITORY=OWNER/VpsCT SECURITY_EPOCH=N` 生成双架构附件及 `SHA256SUMS`。选择可选签名模式的维护者需另行准备并审核 `signing-manifest.json`；默认发行流程不生成或使用该文件。版本、架构、组件、长度、哈希和安全代次写入 TUF targets；哈希清单本身不替代签名。第三方内核必须从上游核对来源、版本与哈希后，作为 `sing-box` / `snell-server` 组件加入同一受审查清单。**不要签署控制端提交的任意文件。** 发布清单保留仍受支持的目标；删除目标阻止新的在线安装。

GitHub `release-signing` 环境需配置 `TUF_ROOT_JSON`、累计撤销策略 `TUF_SECURITY_POLICY_JSON`、正整数 `SECURITY_EPOCH`，以及三个非 root 角色密钥秘密。工作流仅生成草稿附件，不自动发布活跃元数据仓库。发布审批、根公钥指纹交付、内核目录和元数据续期负责人必须由维护者确定。

### 2.1.1 累计撤销策略与恢复

发布工具要求 keys 目录内显式提供 `security-policy.json`；CI 从受保护环境变量读取。首次策略格式如下，发布时由工具设置单调 sequence 和不超过七天的有效期。后续必须保留所有旧撤销摘要与安全底线，不得重置为空。

```json
{"schema":1,"product":"VpsCT","min_epoch":{},"revoked":[]}
```

策略作为普通 TUF target 签名发布。客户端先持久化策略，再允许执行；删除发布目录中的旧版本不能替代精确撤销。控制端验证器还从已签名压缩包建立实际文件清单，回退时重新检查内容。旧安装没有清单时，安装器用独立验证器验证对应原始发行包，再比对已安装文件，全部通过后才建立恢复记录；不能直接为当前目录“补签”。发布目录必须保留并授权对应旧版原始包。

升级不依赖旧程序支持新的验证命令。验证器缺失或可被非 root 用户写入时，在停服前拒绝升级；新验证器仅在服务健康检查成功后原子替换。默认不内置根公钥，也不创建发布密钥。恢复工具与发行包从同一官方来源下载并通过 SHA256 校验，不需要用户提前配置。

升级在停服前检查恢复点；唯一恢复点已撤销、低于新安全底线或文件被修改时不切换。已开始切换的恢复授权仅有效三十分钟，绑定固定来源和摘要，允许策略在切换期间过期，但不忽略新收到的撤销或更高代次。正常在线更新仍须最新有效策略。

元数据续期时保留完整受支持目标和累计策略，先发布目标与编号元数据，最后原子替换 timestamp。生产到期告警与周期续签尚需维护者配置；不得把本地轮换测试当成生产密钥运维已就绪。

### 2.2 每台机器的本机策略

验证器固定安装在 `/usr/local/libexec/ctlvps-verify`，root 拥有、0755，父目录不得组/其他用户可写。独立取得的根文件放在 `/etc/ctlvps/tuf-root.json`，0644。`/etc/ctlvps/security.json` 使用 0600，所有祖先目录 root 拥有且不得组/其他用户可写。

示例策略（按该机器职责删减动作，域名只是占位符）：

```json
{
  "schema": 1,
  "metadata_url": "https://updates.example.com/metadata/",
  "root_file": "/etc/ctlvps/tuf-root.json",
  "min_epoch": {"controller": 1, "agent": 1, "sing-box": 1, "snell-server": 1, "installer": 1, "verifier": 1},
  "actions": ["agent.configure", "agent.update", "core.install", "controller.update"],
  "max_nodes": 256,
  "max_memory_mb": 512,
  "pause_config": false,
  "private_nodes": [],
  "acme_domains": [],
  "certificates": {}
}
```

卸载动作按端使用 `agent.uninstall` / `controller.uninstall`；清空数据还需相应的 `agent.purge` / `controller.purge`。缺失策略或动作时拒绝，不给网页添加跳过开关。`private_metadata: true` 仅用于管理员明确部署的私有元数据来源，默认关闭。

ACME 域名逐个写入 `acme_domains`；外部证书在 `certificates` 登记证书 ID 对应的绝对 `cert`、`key` 路径，面板只传 `cert_id`。`private_nodes` 仅列确需访问私网的节点 ID；它会解除该节点的数据面目的地址限制，须谨慎授权。默认禁止代理访问本机、内网和元数据地址，仅为系统 DNS 留端口 53 例外。

`/var/lib/ctlvps-security` 保存已接受元数据、验证记录和安全代次，root 专有。正常升级、卸载或清空应用数据均不删除它，避免重新接受旧代次；共享策略与验证器也保留，需本机管理员明确退役。不得用删除此目录处理更新错误。

### 2.3 安装与旧版本迁移

先通过独立可信渠道放置本地安装器：控制端 `/usr/local/libexec/ctlvps-install.sh`，agent `/usr/local/libexec/ctlvps-install-agent.sh`。如果取得的是发行附件，使用**已经可信**的验证器验证后再执行：

```bash
sudo /usr/local/libexec/ctlvps-verify verify-release installer vX.Y.Z /staging/install.sh
sudo install -o root -g root -m 0755 /staging/install.sh /usr/local/libexec/ctlvps-install.sh
sudo bash /usr/local/libexec/ctlvps-install.sh --domain panel.example.com
```

agent 安装器同样先验证。面板的安装命令现在调用本地可信脚本；注册令牌 15 分钟、一次性消费，不能把含令牌的命令贴入公开记录。

旧控制端没有 `verify-release` 命令时，不能直接使用旧程序的网页升级建立信任。停止服务并独立备份，使用已可信验证器验证新发行包，再按手动部署流程替换程序及服务配置；旧 agent 则通过可信本地安装渠道更新。先单台验证，再明确选择批次。不要执行旧面板给出的未验证下载管道。

自动回退要求本机保存过验证记录，且旧目标仍满足当前安全代次底线。跨安全代次升级失败会保持停止并报告人工恢复，绝不降低底线。没有历史验证记录的旧部署不承诺自动回退。

## 3. 控制端兼容变化

### 3.1 浏览器与反向代理

配置准确的 `CTLVPS_SITE_URL=https://…`，只信任实际相邻代理的 `CTLVPS_TRUSTED_PROXIES` CIDR。生产后端默认 loopback；公网监听必须有 HTTPS 站点 URL。Cookie 改名后需要重新登录，前后端必须同版更新。脚本客户端访问 cookie 管理 API 也必须提供正确 Origin、JSON 类型及从 `/api/v1/auth/csrf` 获取的 CSRF token。

CSP 禁止内联脚本、第三方脚本和 iframe 嵌套；样式保留内联支持以兼容现有 UI。HTTP 本地开发仅限 loopback。HTTPS 使用 HSTS，但不启用 includeSubDomains/preload。代理不要公开缓存 `/api`、`/s`、`/r`，访问日志不得记录完整订阅路径、查询或请求正文。

### 3.2 外部订阅与历史数据

默认 HTTPS 且只允许公网目的地址。遗留 HTTP 来源用 `CTLVPS_SUBSCRIPTION_HTTP_ORIGINS=http://source.example:80` 精确登记；私网来源另用 `CTLVPS_SUBSCRIPTION_PRIVATE_ORIGINS=https://internal.example:443`。两类例外独立，协议/域名/端口必须匹配，重定向不继承；元数据、链路本地和转换地址仍禁止。不要配置全局内网放行。

升级前在管理页检查外部来源；失败保留旧快照。历史短码不静默轮换，管理员应安排客户端刷新窗口后使用现有轮换操作；新短码至少 24 字符。历史非法头像显示默认头像。已有节点迁移到新 agent 后受数据面限制，私网链式连接需要预先登记节点例外。

## 4. 加密与恢复

### 4.1 密钥保管

systemd 安装使用 `/etc/ctlvps/secrets.key`，32 字节，root:ctlvps 0640；SQLite 与日常备份不携带密钥。Docker 分开挂载 `/data` 和 `/keys`；两卷不能打包后声称密钥分离。手动部署用 `--secrets-key-file` 指向独立受限文件；开发默认同目录密钥仅适合本地开发。

升级会加密可恢复秘密字段并整理旧数据库页；不能消除以前的明文备份或存储介质历史副本。加密保护离线文件泄漏，不能保护已能读取密钥的控制端进程。密钥另做受控离线备份，丢失密钥不能恢复数据。不要将旧数据目录密钥或整个 `/etc/ctlvps` 混入普通备份。

### 4.2 日常数据库恢复

每日备份为 `.db.enc`，默认 7 份，VACUUM INTO 获取一致性快照后分块 AES-GCM 加密。覆盖配置和主数据库，不含连接日志库、环境或 HTTPS 证书。

在可信环境、服务停止时恢复到一个不存在的目标文件：

```bash
sudo /usr/local/libexec/ctlvps-verify backup restore /etc/ctlvps/secrets.key \
  /backup/ctlvps.db.enc /staging/restored.db
```

`restore` 验证完整密文并清理会话、注册令牌、未完成维护任务的回调凭据；错误密钥、截断或已有目标均失败。检查恢复库后设置 ctlvps 所有者与 0600，再在停服状态替换数据库，并处理旧 WAL/SHM。订阅和 agent 凭据不自动轮换；若怀疑泄漏，另行撤销并重新注册。

升级整目录快照为 `data.tar.gz.enc`；`backup open` 只解密归档，不清理数据库凭据，灾难恢复需额外撤销会话与注册令牌。日常升级自动回退仅恢复本机升级前状态。RPO 24 小时、RTO 60 分钟为运维目标，尚非生产 SLA。

## 5. 执行与容量边界

控制端非 root。agent 的 HTTPS/TLS、外部程序下载在 UID/GID 65534 子进程处理，固定 root 父进程执行已验证的配置/安装动作；这不等于整个 agent 都非 root，也不是通用沙箱。core 进程仍有必要的网络能力，使用 systemd 写目录、能力集和资源限制；不提供任意 shell、路径、仓库或服务名接口。

默认预算：Argon2 2 路、普通 API 8 路、agent 16 路、公开订阅渲染 4 路、外部抓取 4 路；SSE 每用户 5/全局 100，25 秒重验；心跳 1 MiB，日志压缩 2 MiB/解压 8 MiB/10,000 条；主库约 1 GiB、连接日志库约 512 MiB。额度不是承诺吞吐量。磁盘满时写入失败，不保证自动清理所有有效业务数据。部署仍需磁盘监控、系统日志轮转和供应商流量防护。
