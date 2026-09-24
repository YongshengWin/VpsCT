# 安装、升级与恢复

v0.1.3 默认使用官方 HTTPS 下载与 SHA256 校验，无需准备发布签名密钥、信任根或验证器。可选自定义签名部署见[安全迁移说明](security-migration.md)。
[返回 README](../README.md)

本文按“选择部署方式 → 完成安装 → 配置与运维 → 更新 → 失败恢复”的顺序组织。除明确标注在被管理 VPS 上执行的步骤外，命令都在控制端服务器运行。

## 1. 选择部署方式

| 方式 | 适用场景 | 后续维护方式 |
|---|---|---|
| 安装器部署 | 新装面板，希望自动配置系统服务 | 下载最新安装脚本，执行 `--update` |
| 手动二进制部署 | 已有安装布局，或希望自行管理服务配置 | 备份后手动替换程序 |
| Docker 部署 | 已使用容器管理服务 | 更新源码、重建镜像并保留数据卷 |

三种方式的升级和恢复步骤不同。安装器会拒绝覆盖现有手动安装或未知数据目录。

以下安装与更新命令使用官方仓库 `YongshengWin/VpsCT` 的最新正式版入口，无需手动填写版本号。需要指定版本时，使用对应 [Release](https://github.com/YongshengWin/VpsCT/releases) 提供的命令。

## 2. 使用安装器部署

### 2.1 检查环境并下载脚本

安装器要求 Debian / Ubuntu、正在运行的 systemd、root 权限，以及 Linux amd64 / arm64。服务器需能访问 GitHub Release 和系统软件源；首次安装要求本机 8080 端口空闲。

先下载最新官方安装器，再选择 HTTPS 配置方式：

```bash
curl -fLsS --proto '=https' --proto-redir '=https' https://github.com/YongshengWin/VpsCT/releases/latest/download/install.sh -o install.sh
```

`latest` 入口会取得当时最新正式版的脚本。脚本随后下载该版本的程序及校验文件，确保本次安装使用同一版本。保存到本地的脚本不会自行变成新版，之后更新时应重新下载。

指定版本是可选用法。例如安装 `v0.1.0`，可把下载地址中的 `latest/download` 换成 `download/v0.1.0`。若使用源码目录中的脚本，首次安装需额外传入 `--repo YongshengWin/VpsCT`，未指定 `--version` 时会解析最新正式版；也可用 `--version v0.1.0` 指定版本。

### 2.2 自动配置 HTTPS

先把域名解析到控制端服务器，确保 80、443 端口空闲且公网可访问，然后执行：

```bash
sudo bash install.sh --domain panel.example.com
```

安装器配置系统服务，并安装 Caddy 提供 HTTPS。已有 Caddy 配置或 80、443 端口被占用时，改用下一节的方式；安装器不会覆盖已有站点或自动修改防火墙。

域名开启 Cloudflare 橙云时，还需按 [第 2.6 节](#26-cloudflare-设置与访问排查) 配置“完全（严格）”回源，避免循环跳转。

### 2.3 使用已有 HTTPS 入口

已有 Caddy、Nginx 等 HTTPS 入口时，执行：

```bash
sudo bash install.sh --site-url https://panel.example.com --no-proxy
```

将该站点的后端指向 `127.0.0.1:8080`，保留 Host，正确传递访问者地址，并允许 SSE 长连接。公网访问应经过这个可信入口。

Nginx / 宝塔请在该站点实际生效的反向代理 `location` 中配置如下内容；已有同名指令时替换原行，不要重复添加。TLS 证书仍由现有 HTTPS 站点配置管理。

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $http_host;
    proxy_set_header X-Forwarded-Host $http_host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_http_version 1.1;
    proxy_set_header Connection "";
    proxy_buffering off;
    proxy_cache off;
    proxy_read_timeout 3600s;
}
```

保存后检查 Nginx 配置并重载。`CTLVPS_SITE_URL` 必须是浏览器实际访问的公网 HTTPS 地址，不能填后端 `http://127.0.0.1:8080`。非默认公网端口需要包含在站点地址中；HTTPS 的省略端口与 `:443` 等价。

如果代理必须改写 Host，可以通过 `X-Forwarded-Host` 传递原始域名，但控制端只接受 `CTLVPS_TRUSTED_PROXIES` 中实际相邻代理发送的单一值，且该值仍须匹配 `CTLVPS_SITE_URL`。代理必须像上例一样**覆盖**这个头，不能透传客户端提供的值或追加列表。来自非可信地址的该头会被忽略；可信代理提供空值、多值或错误域名时会被拒绝。未配置站点地址的本地开发不使用该头。

脚本部署已信任本机回环代理；Docker 内看到的相邻代理地址可能是网桥网关，需按实际连接配置精确 CIDR，不能直接照抄回环地址或放开所有来源。修改脚本部署的 `/etc/ctlvps/ctlvpsd.env` 后重启 `ctlvpsd`；修改 Compose 环境变量后，在 `deploy` 目录执行 `docker compose up -d --force-recreate ctlvpsd`。

遇到 `421 invalid_host` 时，先核对站点配置与上述 Host 设置。在服务器本机运行以下命令（将示例域名替换为实际域名，非默认公网端口也需带上）：

```bash
curl -i http://127.0.0.1:8080/ -H 'Host: panel.example.com'
```

如果本机请求不再报 `invalid_host`，而公网访问仍报错，继续检查代理链路的 Host / X-Forwarded-Host 配置。`/healthz` 跳过 Host 不匹配检查，不能用于验证域名配置。未保留 Host、也未传递可信转发域名的旧代理配置仍需按上例调整。

`--domain` 和 `--site-url ... --no-proxy` 是两种安装模式，选择其中一种。

### 2.4 验证安装并创建管理员

先检查控制端服务和本地健康接口：

```bash
sudo systemctl status ctlvpsd
curl -fsS --noproxy '*' http://127.0.0.1:8080/healthz
sudo cat /opt/ctlvps/data/setup-token
```

再通过浏览器访问面板域名，确认 HTTPS 正常，输入一次性初始化令牌并创建管理员。随后按 [README 的接入步骤](../README.md#13-接入服务器) 为被管理的 VPS 安装 agent。

初始化成功后令牌失效并删除。若尚未创建管理员且令牌丢失，可停止控制端、移除 `setup-token`、重启后重新获取；已有管理员的安装不会重新开放初始化。

### 2.5 安装器管理的目录

| 路径 | 用途 |
|---|---|
| `/opt/ctlvps/releases/` | 各次安装的版本目录，由 root 管理 |
| `/opt/ctlvps/current` | 当前版本软链接 |
| `/opt/ctlvps/ctlvpsd`、`/opt/ctlvps/agents` | 分别指向当前版本的控制端和 agent 文件 |
| `/opt/ctlvps/REPOSITORY` | 安装器记录的下载仓库 |
| `/opt/ctlvps/data/` | 账户、配置、用量和日志数据 |
| `/etc/ctlvps/ctlvpsd.env` | 控制端环境配置 |
| `/etc/systemd/system/ctlvpsd.service` | 控制端服务定义 |
| `/etc/systemd/system/ctlvps-maintenance.service` | 新版安装器配置的专用维护服务，以 root 执行固定维护任务 |
| `/run/ctlvps-maintenance/control.sock` | 本地维护通道，仅供 root 和 ctlvps 组访问，不开放网络端口 |
| `/var/lib/ctlvps-maintenance/` | root 管理的任务状态与维护日志，独立于两端的数据目录 |
| `/opt/ctlvps/backups/pre-upgrade-*/` | 每次升级前的数据、配置、服务定义和旧版本路径 |

已下载完整发行附件时，可增加 `--assets-dir /path/to/release --version v0.1.0`。该目录需包含对应架构的压缩包和 `SHA256SUMS`；安装仍会访问系统软件源准备依赖。

### 2.6 Cloudflare 设置与访问排查

#### 2.6.1 开启橙云时的配置

Cloudflare 的“灵活 / Flexible”模式通过 HTTP 连接源站，而安装器配置的 Caddy 会将 HTTP 跳转到 HTTPS。两者同时启用会造成循环重定向。使用“仅 DNS”（灰云）时，请求直接到达服务器，无需配置 Cloudflare 回源模式。参见 [Cloudflare 循环重定向说明](https://developers.cloudflare.com/ssl/troubleshooting/too-many-redirects/)。

开启橙云时，按以下顺序检查：

1. 确认源站 HTTPS 已就绪：Caddy 已取得未过期、域名匹配的有效证书，且 Cloudflare 能连接源站 HTTPS 端口。浏览器看到的 Cloudflare 边缘证书，不能代替源站证书。详见 [完全（严格）的要求](https://developers.cloudflare.com/ssl/origin-configuration/ssl-modes/full-strict/)。
2. 在 Cloudflare 的「规则 → 概述 → 创建规则 → 配置规则」中，使用表达式 `(http.host eq "panel.example.com")`，将示例域名替换为实际面板域名；只添加 **SSL** 设置，选择 **严格 / Strict** 并部署。这相当于为该子域设置“完全（严格）”，不会改变其他子域的加密模式。参见 [配置规则的 SSL 设置](https://developers.cloudflare.com/rules/configuration-rules/settings/#ssl)。
3. 如果整个主域下的站点均满足严格加密要求，也可在「SSL/TLS → 概述」中统一选择 **完全（严格） / Full (strict)**。不要把“自动模式已启用”当作已生效的严格模式，应核对当前模式及匹配面板子域的规则。

#### 2.6.2 验证公网访问

安装器检查的是控制端本机健康接口；`Valid configuration` 只表示 Caddy 配置有效。两者均不能证明公网 DNS、证书和 Cloudflare 回源已经正常。

先在控制端确认本机服务，再从自己的电脑检查公网入口。将示例域名替换为实际面板地址；使用自定义 HTTPS 端口时，同时补上端口：

```bash
# 在控制端服务器执行
curl -fsS --noproxy '*' --max-time 10 http://127.0.0.1:8080/healthz

# 在自己的电脑执行
curl -fsSL --max-redirs 5 --max-time 20 https://panel.example.com/healthz
```

公网健康接口应返回 HTTP 200；再用浏览器打开面板，确认能显示管理员初始化或登录页面。

| 现象 | 优先检查 |
|---|---|
| 重定向次数过多、连续 301 / 308 跳回同一个 HTTPS 地址 | Cloudflare 是否仍为“灵活”模式，子域配置规则是否匹配；同时检查是否存在冲突的跳转规则 |
| Cloudflare 525 / 526，或直接访问时出现证书错误 | 源站 HTTPS 握手、证书有效期与域名匹配情况；查看 `sudo journalctl -u caddy -n 80 --no-pager` |
| 连接超时 | 域名解析、VPS 防火墙、云平台安全组与入口端口是否放通 |
| 502 / 503 | `ctlvpsd` 是否运行、本机健康接口是否正常，以及 HTTPS 入口的后端地址是否正确 |

## 3. 其他部署方式

### 3.1 手动二进制部署

1. 下载匹配服务器架构的 Release 压缩包和 `SHA256SUMS`，核对该压缩包的校验值后解压。
2. 创建专用的 `ctlvps` 系统用户和用户组，准备 `/opt/ctlvps/data`，授予该用户读写权限。
3. 将 `ctlvpsd` 放到 `/opt/ctlvps/ctlvpsd`，将 `agents/` 放到 `/opt/ctlvps/agents/`；目录应可遍历，程序设为可执行。
4. 创建 `/etc/ctlvps/`，将发行包中的 `ctlvpsd.env.example` 复制为 `ctlvpsd.env`，设为 root 所有、权限 `0600`，填写实际 `CTLVPS_SITE_URL`。
5. 将 `ctlvpsd.service` 复制到 `/etc/systemd/system/`，运行下面的命令启动服务。

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now ctlvpsd
```

自行配置 HTTPS，再按第 2.4 节检查服务并创建管理员。这里使用发行包内的文件；直接使用源码时，对应文件位于 `deploy/`。

### 3.2 Docker 新安装

先获取目标版本源码，进入项目目录，编辑 `deploy/docker-compose.yml` 中的 `CTLVPS_SITE_URL`，再执行：

```bash
cd deploy
docker compose up -d --build
docker compose exec ctlvpsd cat /data/setup-token
```

默认镜像以 UID 10001 运行，使用命名数据卷，端口只映射到本机 `127.0.0.1:8080`。公网访问需自行配置 HTTPS；管理员初始化方式与第 2.4 节相同。

### 3.3 保留已有 Docker 数据

已有部署若使用 `./data:/data`，继续保留该挂载，并保证数据目录对 UID 10001 可写。直接改为一个空命名卷会启动全新的面板数据。

迁移到命名卷时，按顺序操作：

1. 停止旧容器，备份原数据目录。
2. 创建目标卷，复制完整数据并设置正确所有者。
3. 切换挂载并启动，确认管理员、服务器、配置和分享记录均存在。
4. 验证完成后再决定是否清理旧副本。

## 4. 配置与日常运维

### 4.1 控制端配置

产品和仓库名为 VpsCT；控制端程序为 `ctlvpsd`，VPS 上的管理程序为 `ctlvps-agent`，数据目录和系统服务沿用 `ctlvps` 命名。

| 环境变量 | 用途 |
|---|---|
| `CTLVPS_LISTEN` | 控制端监听地址；安装器默认设为 `127.0.0.1:8080` |
| `CTLVPS_DATA_DIR` | 数据与备份目录 |
| `CTLVPS_SITE_URL` | 面板公网地址，用于域名与来源校验、生成安装命令和访问链接 |
| `CTLVPS_AGENT_BIN_DIR` | 提供给 VPS 下载的 agent 文件目录 |
| `CTLVPS_TRUSTED_PROXIES` | 实际相邻代理的 CIDR，逗号分隔；限定访问者地址和转发域名等头的信任来源，见第 2.3 节 |
| `CTLVPS_TRUST_PROXY` | 旧兼容开关；未指定 `CTLVPS_TRUSTED_PROXIES` 时仅信任回环地址 |
| `CTLVPS_DISABLE_CONNLOG` | 关闭控制端连接日志存储和接收 |
| `CTLVPS_ONLINE_GEOIP` | 主动开启第三方在线 IP 归属地查询，默认关闭 |
| `CTLVPS_LOG_LEVEL` / `CTLVPS_LOG_JSON` | 日志级别与格式 |

完整选项运行 `ctlvpsd --help` 查看。命令行参数优先于环境变量；修改环境配置后需重启服务。数据与外部请求范围见 [隐私说明](privacy.md)。

### 4.2 查看状态与日志

systemd 部署使用：

```bash
sudo systemctl status ctlvpsd
sudo journalctl -u ctlvpsd -f
```

Docker 部署在源码的 `deploy/` 目录使用 `docker compose ps` 和 `docker compose logs -f ctlvpsd`。

### 4.3 备份数据

控制端每天备份主数据库为加密 `.db.enc`，密钥单独保存；[恢复命令及凭据撤销](security-migration.md#42-日常数据库恢复)。默认保留 7 份，目录为数据目录下的 `backups/`。这类自动备份不包含独立的连接日志库、环境文件和服务定义。

完整迁移前，应停止控制端，备份整个数据目录以及环境、服务和 HTTPS 配置。不要只复制运行中的 SQLite 主 `.db` 文件而遗漏 WAL 文件。备份、环境文件和访问链接均应限制访问权限。

### 4.4 同机部署与服务连接

控制端与 agent 可以运行在同一台 VPS 上：先完成控制端安装，再把这台机器添加到「服务器」，执行面板生成的 agent 安装命令。

1. **区分两个地址**：面板地址用于登录、管理和 agent 上报；「服务器 → 编辑 → 公网地址」用于客户端连接该服务器提供的服务。公网地址应填写直连公网 IP 或仅 DNS（灰云）域名。
2. **避免橙云地址误用**：普通 Cloudflare 橙云只转发受支持的 HTTP/HTTPS 流量，不能直接转发任意服务端口。面板使用橙云时，为服务使用独立的灰云域名或直连 IP。详见 [Cloudflare 端口说明](https://developers.cloudflare.com/fundamentals/reference/network-ports/)。
3. **检查端口**：服务端口不能与面板 HTTPS 入口等已有进程冲突。自动分配范围为 `20000–49999`；手动指定时也要确认端口空闲，并按服务使用的 TCP/UDP 类型放行主机防火墙与供应商安全组。agent 无需管理入站端口，不代表它部署的服务无需开放端口。

   agent 部署成功后，会在原生 nftables 的 `inet filter input` 链中自动放行当前节点（含分享节点）所需的 TCP/UDP 端口。改端口、停用或删除节点会同步清理本功能创建的旧规则；重启或防火墙重载后，agent 会在配置收敛或心跳时补回规则。只管理带 `ctlvps-node-ingress:` 标记的规则，不修改 SSH 和其他服务规则，也不写入系统防火墙配置文件；卸载 agent 时清理这些规则。其他防火墙管理器暂不自动修改。对 iptables/ip6tables 的 INPUT 兼容链，会只读核对 nftables 与兼容规则：默认允许且端口范围与节点不重叠的 multiport 跳转（例如仅保护 SSH 的 Fail2ban）不会阻止配置应用。默认拒绝、端口重叠、未知规则或无法核对时仍提示手动放行。供应商安全组仍需手动配置，主机规则同步成功不等于公网连通性验证通过。
4. **分层排查超时**：先确认 agent 在线、配置版本已同步，再到「诊断」确认服务实例运行；随后核对客户端使用的地址、端口和公网连通性。配置下发成功、进程运行中，都不能单独证明客户端能够连接。
5. **地址修改后更新客户端**：保存服务器公网地址会更新其已部署节点的连接地址。客户端需刷新订阅；手动导入的节点需重新导入，旧链接中的地址不会自动改变。

### 4.5 卸载能力

发行附件中的独立卸载器支持控制端、agent 和两端同机的默认 systemd 安装，兼容早期 `v0.1.0`。命令与数据保留规则见 [卸载与清理](#7-卸载与清理)。

### 4.6 多网卡、出口和托管中转

在「服务器 → 节点线路」配置网络。操作顺序、计费口径、托管中转、固定端口映射及故障处理统一见 [网络与中转使用指南](network.md)；私网端点另见 [本机传输授权](network-transport-authorization.md)。节点客户端格式、模板和分享继承见 [节点、订阅与分享](subscriptions.md)。

### 4.7 内核版本与升级确认

VpsCT 只使用官方 sing-box 发行包，不构建或分发修改版。网卡绑定配置支持官方 1.12.14 起的 1.12.x、1.13.x、1.14.x 稳定系列，系列内补丁升级不再锁死单个版本；SS-2022 出口和托管 SS-2022 要求官方 1.14.1 或兼容的 1.14.x。跨入尚未适配的新系列或预发布版本时，有网络资源的配置会拒绝切换。上述范围表示配置兼容策略，不代表每个版本及架构均完成流量验收；当前实际网络基线为 ARM64 官方 1.14.1，AMD64 运行矩阵仍需验收。

到「设置 → 内核版本」选择并保存官方版本，等待 agent 上报安装版本和配置回执。新安装默认锁定官方 sing-box 1.14.1；已有数据库升级时，显式版本选择保持不变，未设置或留空的旧默认值固定保存为 1.12.14。迁移在启动时以事务执行，重复启动不会重新选择版本；读取旧设置失败时停止迁移，不以新默认值覆盖。主动选择默认版本并保存，也会记录当次具体版本，不会因浏览页面、创建草稿或后续升级面板而自动跟随最新内核。官方新版 Linux 默认发行包需要兼容的 glibc 运行环境；本次运行验证使用 Ubuntu 24.04，不支持把该包当成 Alpine 静态二进制直接执行。

管理员页面会按实际兼容状态显示内核升级建议；没有使用 sing-box 的受管服务器时不显示。关闭偏好按管理员账号、当前浏览器和建议版本保存，同一建议不会因面板升级再次弹出。「设置 → 内核版本」始终可查看各服务器的实际版本、等待应用、离线和异常状态，也可重新显示提示。选择推荐版本只修改待保存表单；保存后才下发。只有在线服务器回报匹配的实际版本、最新配置应用回执和正常运行状态后，才标记已确认并隐藏提示；此检查不代替节点连通测试。

## 5. 更新

### 5.1 安装器部署的更新

重新下载最新安装器后升级：

```bash
curl -fLsS --proto '=https' --proto-redir '=https' https://github.com/YongshengWin/VpsCT/releases/latest/download/install.sh -o install-vpsct.sh &&
sudo bash install-vpsct.sh --update --auto-rollback
```

升级保留站点配置，不同时传入域名或 HTTPS 模式参数。需要升级到指定版本时，使用对应 Release 的脚本。已有本地脚本也支持 `--version latest`，但它只改变程序下载版本，不会更新脚本本身，因此推荐上面的命令。

安装器先校验发行包，随后停止控制端、备份完整数据、切换版本并检查健康状态。账户、配置和分享不会重新初始化。它不支持直接降级；需要恢复旧版时使用对应版本的数据备份。

新版脚本支持 `--update --auto-rollback`：启动失败后尝试恢复旧版本、环境配置和停服时的数据快照，并重新检查健康状态。自动恢复成功时退出码为 `20`，原始升级仍视为失败。网页升级自动使用此选项；自定义数据目录及数据目录内存在挂载点时拒绝自动恢复。

### 5.2 手动部署的更新

先准备新程序并校验文件，停止控制端，备份数据、旧程序和服务配置，再替换程序并重启。保留自己的安装路径和环境参数。

若同时更换服务定义，先准备它要求的环境文件和目录权限。只更新控制端时保留原 agent 文件；替换 agent 文件会触发下一节所述的自动同步。

### 5.3 Docker 部署的更新

先备份数据与当前镜像信息，再将本地源码更新到目标版本。进入更新后源码的 `deploy/` 目录，保留原站点配置和数据挂载，然后执行：

```bash
docker compose up -d --build
```

这条命令根据本地源码构建，不会自行获取新源码。保留旧镜像和对应数据快照以便恢复；`docker compose down -v` 会删除命名卷，不用于普通升级。

### 5.4 agent 与网页端操作

控制端每次收到心跳时，会比较 agent 的文件校验值。支持自动更新的 agent 与控制端提供的文件不同时，会自动下载、校验、替换并重启。切换完整发行包或 Docker 镜像，也可能因此更新已接入的 agent。

| 网页操作 | 实际作用 |
|---|---|
| 检查 agent 更新 | 查看是否已同步控制端提供的版本；同步由心跳自动进行 |
| 更多 → 重新应用节点配置 | 让该 VPS 重新应用当前节点配置 |

服务组件版本需在设置中选中并保存后才会改变。控制端短暂停服本身不会停止 VPS 上已运行的服务；agent 更新或重新应用配置可能引起服务调整。

### 5.5 从网页升级与卸载

**2026-09-15 更新的 `v0.1.0` 附件包含以下能力。** 此前安装的用户需先执行第 5.1 节的终端更新命令；即使版本号相同，也会安装新文件及维护服务。只修改网页文件不能启用系统维护。

1. **控制端**：安装器会配置 `ctlvps-maintenance.service`。进入「设置 → 系统 → 控制端维护」，检查最新正式版本，选择升级或卸载。控制端仍以普通用户运行；专用维护服务只通过本机 Unix socket 接受固定操作，下载仓库来自 root 管理的安装记录，网页不能指定命令、文件路径或其他下载来源。
2. **agent**：先让 agent 更新到支持维护协议的版本并上线，再进入「服务器详情 → 更多 → agent 维护」。升级同步控制端提供的文件，不独立追踪其他仓库；文件校验值一致时显示「已同步」。下载校验或新进程检查失败时保留或恢复旧程序；自动同步同一失败文件不会无限重试，可从网页手动重试。执行中或异常任务在详情页显示简短提示，已完成记录在维护弹窗中展开查看。
3. **确认范围**：每次手动操作需要管理员密码，启用两步验证的账户还需验证码或恢复码。卸载还要输入 `VpsCT` 或完整服务器名称。默认保留数据，清空数据和清理独占 HTTPS 站点需另行勾选。agent 卸载会停止它部署的服务，资源分享随之停止，控制端和其他 VPS 保留。
4. **查看结果**：任务显示等待、执行中、完成、失败、已回退或结果待核实。同一机器同时执行一个维护任务。agent 超过 15 分钟未领取的任务失效，离线恢复后不会执行旧卸载；已领取任务由独立 systemd 进程运行，网页或 agent 退出不会终止它。网络恢复后可重新查看记录。
5. **页面断开**：升级期间面板会短暂断开；卸载控制端后网站将不可用。连接中断不等于卸载成功。独立 worker 写入最终状态，agent worker 使用只允许上报当前任务的临时凭据回传结果；回传失败时会重试，页面无法确认的结果应在目标服务器上核实。
6. **适用安装**：自动操作支持 Debian / Ubuntu 的默认 systemd 安装。自定义路径、服务覆盖配置、Docker 部署不支持此网页执行器，页面会提示维护服务不可用或任务预检查失败。无需为维护服务额外开放端口。

任务展开项提供终端查看命令。将任务编号替换成页面显示的值：

```bash
sudo cat /var/lib/ctlvps-maintenance/任务编号/status.json
sudo cat /var/lib/ctlvps-maintenance/任务编号/worker.log
sudo journalctl -u ctlvps-maintenance
```

维护记录由 root 保存，升级、重启和应用数据清空后仍可诊断。结束后删除执行副本和临时报告凭据；安全结果和诊断日志保留。agent 升级及回退都失败时，还保留该任务的 `agent.previous` 供终端恢复。控制端备份位于 `/opt/ctlvps/backups/`。机器断电或 worker 被终止时，不会自动重放卸载；任务标为结果待核实。

## 6. 更新失败后的恢复

### 6.1 恢复安装器部署

切换版本前失败时，安装器会尝试重新启动原服务；已经切换后失败时，会停止控制端并给出备份位置，需要恢复旧程序及其对应的数据快照。升级备份不自动删除。

下面的命令**仅适用于本安全版本安装器生成的加密备份**。仅做本机升级失败回退；灾难恢复后还必须撤销旧会话和注册令牌。先进入 root 终端，把 `restore_backup` 改为实际备份目录；命令会保留失败现场，恢复后的数据以备份时间为准。

```bash
sudo -i
```

在 root 终端继续执行：

```bash
set -e
restore_backup=/opt/ctlvps/backups/pre-upgrade-替换为实际目录
for restore_file in data.tar.gz.enc ctlvpsd.env ctlvpsd.service previous-release; do
  test -f "$restore_backup/$restore_file"
done
previous_release=$(cat "$restore_backup/previous-release")
test -x "$previous_release/ctlvpsd"
test -f "$previous_release/REPOSITORY"
/usr/local/libexec/ctlvps-verify verify-rollback controller "$(cat "$previous_release/VERIFIED-SHA256")"
restore_tmp=$(mktemp -d)
chmod 0700 "$restore_tmp"
/usr/local/libexec/ctlvps-verify backup open /etc/ctlvps/secrets.key "$restore_backup/data.tar.gz.enc" "$restore_tmp/data.tar.gz"
tar -tzf "$restore_tmp/data.tar.gz" >/dev/null
systemctl stop ctlvpsd
mv /opt/ctlvps/data "/opt/ctlvps/data.failed.$(date -u +%Y%m%dT%H%M%SZ)"
tar -xzf "$restore_tmp/data.tar.gz" -C /opt/ctlvps
rm -rf -- "$restore_tmp"
install -m 0600 "$restore_backup/ctlvpsd.env" /etc/ctlvps/ctlvpsd.env
install -m 0644 "$restore_backup/ctlvpsd.service" /etc/systemd/system/ctlvpsd.service
install -m 0644 "$previous_release/REPOSITORY" /opt/ctlvps/REPOSITORY
restore_link="/opt/ctlvps/current.restore.$$"
ln -s "$previous_release" "$restore_link"
mv -Tf "$restore_link" /opt/ctlvps/current
systemctl daemon-reload
systemctl start ctlvpsd
restored_listen=$(sed -n 's/^CTLVPS_LISTEN=//p' /etc/ctlvps/ctlvpsd.env)
restored_ready=0
for restore_attempt in $(seq 1 30); do
  if curl -fsS --noproxy '*' --max-time 2 "http://$restored_listen/healthz"; then
    restored_ready=1
    break
  fi
  sleep 1
done
test "$restored_ready" = 1
```

恢复后检查 HTTPS、登录、服务器状态和分享记录。恢复旧发行包也会恢复它提供的 agent 文件，已接入的 agent 可能随心跳同步回该版本。

### 6.2 恢复手动或 Docker 部署

使用该部署方式自行保存的旧程序或镜像、环境配置和完整数据快照。先停服，保留失败现场，再恢复同一时间点的程序与数据，最后启动并验证。

这两种方式的备份布局由部署者管理，不使用第 6.1 节的安装器恢复命令。

## 7. 卸载与清理

### 7.1 选择卸载范围

使用发行附件或仓库根目录的 [uninstall.sh](../uninstall.sh)，在**要卸载的那台 VPS** 上以 root 运行。它不依赖控制端在线，也不需要注册令牌。安装器还会在控制端提供 `/opt/ctlvps/uninstall.sh` 入口。

只有 Agent 的 VPS 也支持独立卸载。新版 Agent 安装器在安装或终端更新时，会从同一官方发行包下载并校验卸载脚本，保存为 `/usr/local/libexec/ctlvps-agent-uninstall.sh`。即使面板记录已删除、控制端离线，也可 SSH 登录该 VPS 后执行：

```bash
# 先预览；--purge 表示连同 Agent 配置、凭据和数据一起清理
sudo bash /usr/local/libexec/ctlvps-agent-uninstall.sh --agent --purge --dry-run
# 确认范围后执行，终端会要求输入 uninstall
sudo bash /usr/local/libexec/ctlvps-agent-uninstall.sh --agent --purge
```

旧版没有该文件时，无需重新注册或安装控制端，从官方发行附件获取脚本即可：

```bash
vpsct_uninstaller="$(mktemp)" &&
curl -fLsS --proto '=https' --proto-redir '=https' --max-time 120 https://github.com/YongshengWin/VpsCT/releases/latest/download/uninstall.sh -o "$vpsct_uninstaller" &&
sudo bash "$vpsct_uninstaller" --agent --purge --dry-run
# 预览无误后，在同一终端执行
sudo bash "$vpsct_uninstaller" --agent --purge
```

去掉 `--purge` 可保留 Agent 配置和数据，也会保留独立卸载脚本以便以后清理。`--agent` 不会卸载同机控制端。面板删除弹窗取消“同时卸载”后，也提供这些命令供保存。

1. 确定只卸载控制端（`--controller`）、只卸载 agent（`--agent`），还是卸载本机两端（`--all`）；三者只能选一个。
2. 先加 `--dry-run` 查看计划，它不会停服或删除文件。
3. 确认范围后，加 `--yes` 执行；省略该选项时，终端会要求输入 `uninstall` 确认。管道或其他非交互运行必须指定 `--yes`。

```bash
# 预览控制端卸载范围
sudo bash uninstall.sh --controller --dry-run

# 卸载控制端，保留配置与数据
sudo bash uninstall.sh --controller --yes

# 卸载 agent 及其部署的服务，保留配置与数据
sudo bash uninstall.sh --agent --yes
```

脚本先停止并禁用相应服务，再删除程序与服务定义。agent 卸载还会停止其部署的全部服务实例，并清理专用 nftables 表 `inet ctlvps`、`inet ctlvps_nodes` 以及生成的代理资源与计量 slice。服务停止失败时会中止文件删除。

### 7.2 同时清空配置与数据

`--purge` 会永久删除所选端的配置、凭据、数据、日志和默认目录内备份。它也可以在一次保留数据的卸载后单独再执行。

```bash
# 只清理控制端，保留同机 agent
sudo bash uninstall.sh --controller --purge --yes

# 只清理 agent，保留同机控制端
sudo bash uninstall.sh --agent --purge --yes

# 清理本机两端
sudo bash uninstall.sh --all --purge --yes
```

| 角色 | 默认卸载删除 | 加 `--purge` 额外删除 |
|---|---|---|
| 控制端 | `ctlvpsd.service`、发行目录、控制端程序与分发软链接、卸载入口、仓库标识 | `/opt/ctlvps/data/`、`/opt/ctlvps/backups/`、`/etc/ctlvps/ctlvpsd.env` |
| agent | `ctlvps-agent.service`、本项目的服务实例与模板、agent 和服务程序、专用 nftables 计量表 | `/var/lib/ctlvps-agent/`、`/var/log/ctlvps/`、`/etc/ctlvps/sing-box/`、共享进程的 `sing-box.json`、`/etc/ctlvps/snell/` |

两端共用 `/opt/ctlvps` 和 `/etc/ctlvps` 的部分父目录。卸载器按角色清理文件，只会移除已经为空的共享父目录。

### 7.3 移除自动生成的 HTTPS 站点

默认保留 HTTPS 配置。如果 Caddy 仍使用安装器生成的独占站点配置，可随控制端清空数据一起添加 `--purge --remove-caddy`：

```bash
sudo bash uninstall.sh --controller --purge --remove-caddy --yes
# 同机两端一起卸载时，将 --controller 改为 --all。
```

卸载器只接受带安装器标记、结构未改动的单站点 Caddyfile。符合时停用 Caddy，删除该配置及默认 Caddy 证书目录中该域名的证书。`--remove-caddy` 必须与 `--purge` 一起使用。发现其他站点或改写过的配置，会在停服前中止；此时去掉 `--remove-caddy` 卸载程序，再自行处理 HTTPS 配置。

### 7.4 自动清理的边界

1. **只操作本机默认 systemd 安装**：Docker、自定义启动路径或数据目录、systemd 覆盖配置需按实际部署手动清理；脚本遇到相关定制或清理范围内的挂载点会拒绝删除。
2. **保留系统通用资源**：不删除 `ctlvps` 系统账户、Caddy/nftables/chrony 等软件包，也不清空其他防火墙表。Caddy 的全局运行数据与缓存保留。
3. **外部资源另行处理**：手动另存的备份、操作系统日志、独立维护任务结果与日志、DNS、安全组规则、面板内记录和其他 VPS 不会被删除。面板删除服务器时默认勾选“同时卸载”，取消勾选才仅删除记录。同时卸载需要管理员二次认证，卸载成功才自动删除服务器和关联节点记录。离线、失败或结果待确认时保留记录，关闭页面不影响任务。卸载默认保留 VPS 配置和数据，可在确认时选择清空。执行中的任务会阻止单独删除记录或重置注册令牌。
4. **不沿软链接删除目标**：选中路径本身为软链接时，只删除链接；父目录为软链接时拒绝自动处理。链接外的数据需另行核对。

### 7.5 只停用而不卸载

如果只是暂时停用面板，可运行：

```bash
sudo systemctl disable --now ctlvpsd
```

Docker 部署可在 `deploy/` 目录运行 `docker compose stop ctlvpsd`。

需要暂时停用某台 VPS 的全部服务时，先在「服务器 → 编辑」关闭「启用」，等待配置同步并确认服务已停止，再执行：

```bash
sudo systemctl disable --now ctlvps-agent
```

单独停用 agent 只停止管理进程，已部署服务由独立 systemd 单元运行，可能继续提供服务。控制端已不可用时，需要在 VPS 本地逐项确认并停止相关服务，不能依赖面板删除操作。

## 8. 代理权限隔离

加固后的代理使用专用低权限账户，管理文件仍由管理进程持有。开机必须先由 agent 安装出站规则，代理才会启动；不要为了启动失败而手工赋予代理 root 或 NET_ADMIN。无法兼容的内核或证书组合会报告失败，不能当作已经加固。

首次身份切换会短暂重启代理。证书快照、原生 ACME 状态、私网分组及恢复行为见 [代理隔离实施记录](security-proxy-hardening-implementation.md)。该记录同时列出已完成测试和上线前仍需验证的项目。

## 9. Agent 内存边界

### 9.1 常驻数据与日志

agent 只持有当前配置、上次指标采样及待上传连接事件，不在内存保存监控历史。两路连接日志共用一个固定环形队列，最多 8,192 条，字符串有效载荷最多 3 MiB，加上小于 1 MiB 的固定槽位，队列预算约 4 MiB。每路连接配对缓存最多 256 条；实际堆占用还包含解析临时对象和 Go 运行时，不能把队列预算当成 RSS 上限。队列满时丢弃旧事件，流量配额计量不依赖这些连接日志。

日志按每轮最多 1 MiB、单行小于 16 KiB 读取，超长行跳过，积压超过一轮预算时从最新 1 MiB 的下一条完整记录继续（连接日志可能丢弃，流量计量不受影响）；解析后的地址最长 1,024 字节，连接 ID 最长 32 字节。环形队列只复制上传批次，避免复制全部积压。关闭连接日志时清空内存积压，空闲时也清理过期配对。内核 OOM 诊断只查询匹配记录，最多 1,000 条；不会把整天的内核日志读进内存。

当前节点上限 256 个，配置响应最多 2 MiB，普通控制响应最多 256 KiB。控制请求、连接日志各有一个独立执行槽，忙时直接返回，不堆积等待请求。systemctl 标准输出最多 1 MiB，nft 最多 4 MiB，标准错误最多 16 KiB，命令最长 30 秒。超限视为失败，不拿截断结果进行计量或配置决策。预算常量集中在 `internal/agentbudget`。

### 9.2 升级峰值与磁盘预算

agent 自更新、网页 agent 更新和内核下载使用受限管道直接写入私有临时文件，校验、解压、安装均不把完整二进制读入堆。保留原有 HTTPS 来源限制、校验/签名验证、权限隔离与原子替换；失败不激活未验证程序。agent 下载上限 128 MiB，内核压缩包与二进制各为 200 MiB，tar 总展开预算 256 MiB，ZIP 目录预算 2 MiB、最多 4,096 个条目，不支持 ZIP64 或分卷包。

流式处理以磁盘空间换取较低堆峰值：自更新前检查至少 128 MiB 的额外空间，内核安装前检查 400 MiB，另保留磁盘预算模块要求的系统余量。文件页缓存仍可能计入服务 cgroup 内存；这些改动不承诺整个服务的峰值 RSS 是常量。网络子进程单独设置 32 MiB 的 Go 运行时软预算。

Linux root agent 将内核安装和自更新交给同一个短生命周期资源 worker，最多同时运行一个任务、不设等待队列，失败退避 30 秒，任务最长 5 分钟。主循环得到 pending 后继续心跳，在后续轮次检查结果并继续收敛。内核二进制替换前持久化待激活标记，所有受影响实例成功启动后才移除，避免 worker 完成或 agent 重启后遗漏内核重启。worker 的 Go 软预算为 48 MiB；网络子进程为 32 MiB；主进程在代码中设置 64 MiB。工作完成后子进程退出，释放重任务产生的堆。

安装器默认设置 agent 的 `GOMEMLIMIT=64MiB`、`MemoryHigh=160M`、`MemoryMax=192M`、`TasksMax=128`。常规资源 worker 和网络子进程共享 agent service 的 cgroup 总预算；这不是每个子进程各享 192 MiB。Go 软预算不限制全部进程内存；硬限制触顶仍可能引发 OOM。已有服务的 systemd 配置需通过安装器更新才能得到新设置，单纯替换二进制不会改 unit。

网页 agent 维护需要在 agent 停服后继续完成回滚，因此沿用独立 systemd 服务，单独设置 48 MiB Go 软预算和 192 MiB 硬限制，不计入上面的 agent service 总额。agent 避免它与常规内核安装任务重叠；这两组服务在交接期间仍可同时存在。常驻 agent 检查维护状态时逐条扫描磁盘回执，不把整个维护历史载入内存。

### 9.3 历史计量状态

最多保留 2,048 个节点计量身份，覆盖当前节点和等待最终结算的历史节点。达到边界后拒绝新增身份，已有身份仍可处理；不会为了压低内存删除未结算流量。旧版本已超过边界的记录也不自动裁剪。状态文件读取上限为 4 MiB，超限明确报错。控制端响应和状态文件在完整解码前检查 JSON 结构：最多 128 Ki 个 token、32 层嵌套、单字符串 64 KiB，避免小体积 JSON 在解码后膨胀为大量对象。

历史回收按以下顺序执行：

1. 配置成功收敛后，nft 冻结退役节点计数并丢弃其保留 mark 的流量；Snell 停止旧实例，确认节点 slice 已无任务。
2. 读取最终计量，形成最多 128 个身份、总计划不超过 1 MiB 的不可变批次，先持久化到本地状态文件。
3. 控制端在同一数据库事务内更新流量、基线和批次回执，提交后才回复 ACK。丢失 ACK 时重发同一批次，不重复计费。
4. agent 持久化 ACK，再删除对应 nft counter / Snell slice，最后删除本地历史身份。清理失败或崩溃后只重试幂等清理，未完成前不接受下一份配置；正常心跳继续。

每次仅保留一批结算，不因断网积压更多任务。同一节点身份回收后重建会采用新计量代次，避免将已归零的计数器误当旧基线。控制端必须先升级并声明支持最终结算协议；旧控制端下保持有界历史、不擅自删除。无法读取最终计数的旧 Snell slice 等异常会阻止回收，需要调查恢复，不能手工清空状态文件掩盖未结算流量。

### 9.4 验证方式与测量边界

运行 `bash scripts/check.sh` 做完整检查；运行 `go test -race ./internal/conntail ./internal/agent ./internal/agentwork ./internal/agentnet ./internal/traffic ./internal/core ./internal/secureupdate ./internal/maintenance ./internal/safehttp ./internal/boundedexec` 检查并发回归。回归覆盖 300 轮删除重建、ACK 丢失、清理失败后恢复、事务回滚，以及十万条长地址日志与单槽任务饱和。

`bash scripts/test-meter-container.sh` 检查真实 nft 计量、退役冻结与幂等回收；`bash scripts/test-agent-retirement-container.sh` 检查真实 systemd slice 的最终读数、拒绝清理仍有进程的 slice，以及重复清理。这些测试均在一次性容器运行。

`bash scripts/test-agent-memory-container.sh` 在禁用外网、192 MiB 内存上限且不允许额外 swap 的一次性容器中，用本地 TLS 服务传输 128 MiB 合成文件，检查超限拒绝及隔离 HTTP 响应，并输出进程和 cgroup 内存记录。它不会连接真实 VPS。升级与回滚仍用 `bash scripts/test-maintenance-container.sh RELEASE_DIRECTORY` 验证。

2026-09-17 的本地基准中，积压 20,000 条、每批取出并重试 500 条事件，优化前每轮分配约 3.58 MB，环形队列约 49 KB（减少约 98.6%）。最终版本的受限容器测试中，128 MiB 下载的父进程与 TLS 测试服务合计分配约 308 KiB，测试进程 RSS 峰值约 22.4 MiB；容器峰值约 156.7 MiB，包含文件页缓存，未触发 OOM。另一条下载被故意阻塞时，独立心跳请求约 10 毫秒完成。该延迟只测网络请求，不包含主循环采样和系统命令耗时。这些是合成测试结果，不是生产 agent 的 RSS 或延迟承诺；长期生产曲线仍需部署后观察。
