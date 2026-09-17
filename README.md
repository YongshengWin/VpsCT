# VpsCT

面向个人和小团队的自托管服务器管理面板。

将多台 VPS 接入同一个面板，集中查看运行状态和流量用量、维护服务配置，并按流量配额和有效期分享服务器资源。

本版更新见 [v0.1.4 发布说明](docs/releases/v0.1.4.md)，完整历史见 [变更记录](CHANGELOG.md)。

## 1. 开始使用

先在一台服务器上安装面板，作为**控制端**；再为需要管理的 VPS 安装 **agent**。agent 是运行在服务器上的管理程序，负责应用配置、采集用量，并主动向控制端上报状态。

### 1.1 安装面板

控制端安装器面向 Debian / Ubuntu，要求 systemd，支持 Linux amd64 / arm64。

**自动配置 HTTPS**：准备一个已解析到控制端服务器的域名，并确保 80、443 端口空闲且可从公网访问。

**开启 Cloudflare 橙云时**：面板域名应使用 **完全（严格） / Full (strict)** 加密模式；“灵活 / Flexible”会与 Caddy 的 HTTPS 跳转形成循环，导致“重定向次数过多”。同一主域下还有其他站点时，建议只为面板子域添加配置规则。使用“仅 DNS”（灰云）时无需此设置，具体步骤见 [Cloudflare 设置与访问排查](docs/operations.md#26-cloudflare-设置与访问排查)。

在控制端服务器下载官方安装器并执行，无需准备发布签名密钥：

```bash
curl -fLsS --proto '=https' --proto-redir '=https' https://github.com/YongshengWin/VpsCT/releases/latest/download/install.sh -o install-vpsct.sh &&
sudo bash install-vpsct.sh --domain panel.example.com
```

将 `panel.example.com` 替换为你的面板域名。安装器从 GitHub 官方仓库通过 HTTPS 下载发行包，并检查 SHA256；不需要配置发布签名密钥或签名服务。具体版本以安装器参数为准。

安装器会下载并校验程序、配置系统服务和 HTTPS。服务器无需安装 Go、Node 或源码编译环境。安装完成后，还需通过面板域名确认 HTTPS 可访问；本机服务启动成功不代表公网入口已就绪。已有 HTTPS 入口等部署方式见 [安装文档](docs/operations.md)。

**使用已有 HTTPS 入口或其他端口**：先将入口转发到本机 `127.0.0.1:8080`，再执行以下命令。这里以 `8443` 为例；使用标准 HTTPS 端口时去掉 `:8443`。这种方式无需为安装器腾出 80、443 端口。

```bash
curl -fLsS --proto '=https' --proto-redir '=https' https://github.com/YongshengWin/VpsCT/releases/latest/download/install.sh -o install-vpsct.sh &&
sudo bash install-vpsct.sh --site-url https://panel.example.com:8443 --no-proxy
```

### 1.2 创建管理员

安装完成后，在**控制端服务器**执行以下命令，获取一次性初始化令牌：

```bash
sudo cat /opt/ctlvps/data/setup-token
```

访问面板域名，输入令牌并设置管理员账户。初始化完成后，令牌失效并删除。

### 1.3 接入服务器

登录面板后，按以下顺序接入每台 VPS：

1. 打开「服务器 → 添加服务器」，填写名称、地址和流量配额。
2. 在服务器详情中生成 agent 安装命令。
3. 到**这台被管理的 VPS** 上，以 root 执行生成的命令。
4. 返回面板，确认服务器显示「在线」。

agent 主动连接控制端，因此 VPS 无需额外开放管理端口。服务器上运行的服务仍需按其配置开放相应端口。

**控制端也可以接入为被管理的服务器**，在同一台机器安装 agent 即可。「公网地址」用于访问这台服务器提供的服务，应填写直连公网 IP 或仅 DNS（灰云）域名；面板域名可以开启橙云，但不能因此把它当作所有服务的连接地址。端口与排查步骤见 [同机部署与服务连接](docs/operations.md#44-同机部署与服务连接)。

## 2. 日常管理

服务器上线后，日常操作主要围绕状态、用量和配置展开。

### 2.1 查看状态与用量

在「服务器」中查看 CPU、内存、磁盘、实时网络速率和运行诊断。流量统计区分入站与出站，配额按两者之和计算；可设置用量周期、每月重置日期和通知提醒。

同一 VPS 的 sing-box 节点共用一个进程，并分别计量。**Snell 仍每个独立节点一个进程，节点增多仍会增加内存；整体内存上限可能触发 OOM 并中断连接，不代表消除了重复开销。** 本版尚未实现 Snell 按需启停或空闲回收，详见[容量风险](docs/shared-proxy-accounting.md#17-snell-容量风险)。


### 2.2 部署与维护配置

在服务器详情中部署受支持的服务。面板保存配置，由 agent 在对应 VPS 上应用并上报执行结果。

节点列表支持批量重置已部署节点的凭据。完成后需更新客户端订阅，并确认配置状态为「已应用」；下发成功不代表服务器已完成应用。

需要复用配置时，在「模板」中维护底稿，再在「订阅链接」中选择对应模板，生成配置访问链接。已有配置也可以通过页面提供的 AI 指令改造为模板。

### 2.3 管理账户与访问记录

为管理员启用两步验证并保存恢复码，通过操作审计和连接日志了解管理操作与资源访问情况。数据采集范围和关闭方式见 [隐私说明](docs/privacy.md)。

## 3. 分享服务器资源

需要让他人使用服务器资源时，在「分享」中创建独立的网络服务访问配置。每份分享分别管理凭据、用量和有效期，使用者通过分享链接访问资源，无需获得面板管理员账户。

1. **选择资源**：选择提供资源的服务器和服务。
2. **设置使用范围**：填写流量配额、用量周期和有效期。
3. **交付访问链接**：创建分享，将对应链接交给使用者。

创建后，可在同一页面查看每份分享的流量，并按需暂停、恢复或撤销。分享用量独立统计，便于了解资源分配后的实际使用情况。

## 4. 更新与备份

### 4.1 更新面板

对于使用安装器部署的控制端，在**控制端服务器**执行以下命令，更新到最新正式版：

```bash
curl -fLsS --proto '=https' --proto-redir '=https' https://github.com/YongshengWin/VpsCT/releases/latest/download/install.sh -o install-vpsct.sh &&
sudo bash install-vpsct.sh --update --auto-rollback
```

更新前会停服备份，并保留账户、配置和分享记录。其他部署方式及失败恢复步骤见 [运维文档](docs/operations.md)。

网页升级入口：**设置 → 系统 → 控制端维护**。v0.1.1 先用上面的新版安装命令升级一次。网页升级失败时恢复停服前的旧程序与数据，恢复文件会再次核对完整性。

v0.1.3 将数据库从 schema 9 迁移至 11。升级前保存数据库与密钥；如需回退，应恢复对应版本的数据备份，不能只替换旧程序。

### 4.2 同步 agent 与配置

新版 agent 随心跳同步更新，并对照官方发行校验清单检查下载内容。支持网页维护的新版 agent 会记录进度，升级失败后停止自动重试，供管理员处理：

| 操作 | 用途 |
|---|---|
| 检查 agent 更新 | 查看该 VPS 的 agent 是否已与控制端提供的版本一致 |
| 升级 agent | 在服务器详情的维护区域立即发起同步或重试，查看执行结果 |
| 重新下发配置 | 让该 VPS 重新应用当前服务配置 |

自动同步仅替换 agent 程序。旧安装的 systemd 资源限制及独立卸载脚本，需要通过新版 agent 安装器的终端更新落地。

### 4.3 备份数据

控制端定时备份主数据库，默认保留 7 份。完整迁移或恢复前，应停止控制端并备份整个数据目录。数据库、备份和访问链接包含敏感信息，请妥善保管。

### 4.4 卸载

发行附件提供独立的 [uninstall.sh](uninstall.sh)，支持分别卸载控制端、agent，或卸载本机两端。脚本也适用于早期 v0.1.0 的默认 systemd 安装，无需先升级程序。

在目标 VPS 使用已验证安装包中的本地卸载器，先查看范围，再选择要卸载的一端：

```bash
sudo bash /opt/ctlvps/uninstall.sh --controller --dry-run
sudo bash /opt/ctlvps/uninstall.sh --controller --yes
# 卸载 agent 改用 --agent；卸载本机两端用 --all。
```

默认保留配置和数据；加 `--purge` 会永久删除所选端的数据与默认目录内备份。agent 卸载会停止其部署的服务，控制端卸载会保留同机 agent。HTTPS 站点处理及完整范围见 [卸载文档](docs/operations.md#7-卸载与清理)。

支持网页维护的版本也可在 **设置 → 系统** 卸载控制端，或在 **服务器详情 → agent 维护** 卸载所选 agent。需要管理员密码、已启用的两步验证和目标名称确认。控制端卸载后网站将不可用，最后结果可从服务器终端查看。

删除服务器默认勾选「同时卸载」，通过二次认证并收到卸载成功回执后才删除记录；离线、失败或结果未确认时保留记录。取消勾选仅删除面板记录，VPS 上的程序仍需单独清理。

只有 agent 的 VPS 使用独立入口，无需安装控制端：

```bash
sudo bash /usr/local/libexec/ctlvps-agent-uninstall.sh --agent --dry-run
# 核对范围后，将 --dry-run 改为 --yes；需要清空数据时另加 --purge。
```

旧安装没有此入口时，按 [独立卸载步骤](docs/operations.md#71-选择卸载范围) 下载官方脚本，不需要重新添加服务器。

## 5. 文档与贡献

| 需要做什么 | 查看文档 |
|---|---|
| 共享代理进程、节点流量与迁移验证 | [进程与计量改造](docs/shared-proxy-accounting.md) |
| 选择其他安装方式、修改配置或恢复数据 | [安装、升级与恢复](docs/operations.md) |
| 了解数据采集与隐私设置 | [隐私说明](docs/privacy.md) |
| 查看版本变化 | [变更记录](CHANGELOG.md) |
| 从源码构建或参与开发 | [贡献指南](CONTRIBUTING.md) |
| 私密报告安全问题 | [安全报告流程](SECURITY.md) |
| 查看完整系统的安全边界与实施计划 | [系统安全设计](docs/security-design.md)与[迁移说明](docs/security-migration.md) |
| 维护和发布版本 | [发布流程](docs/releasing.md) |

欢迎通过 [Issue](https://github.com/YongshengWin/VpsCT/issues) 和 [PR](https://github.com/YongshengWin/VpsCT/pulls) 提交问题、改进建议或代码，参与前请阅读 [社区约定](CODE_OF_CONDUCT.md)。

## 6. 许可证

本项目采用 [MIT 许可证](LICENSE)，Copyright (c) 2026 alio。

第三方组件声明见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)，相关软件与数据遵循各自的上游许可条款。
