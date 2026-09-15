# 安装、升级与恢复

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

先下载最新正式版的安装脚本，再选择下面一种 HTTPS 配置方式：

```bash
curl -fsSL https://github.com/YongshengWin/VpsCT/releases/latest/download/install.sh -o install.sh
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
| `CTLVPS_SITE_URL` | 面板公网地址，用于生成安装命令和访问链接 |
| `CTLVPS_AGENT_BIN_DIR` | 提供给 VPS 下载的 agent 文件目录 |
| `CTLVPS_TRUST_PROXY` | 是否信任 HTTPS 入口传来的访问者地址；仅在后端访问受限且入口可信时启用 |
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

控制端每天备份主数据库，默认保留 7 份，目录为数据目录下的 `backups/`。这类自动备份不包含独立的连接日志库、环境文件和服务定义。

完整迁移前，应停止控制端，备份整个数据目录以及环境、服务和 HTTPS 配置。不要只复制运行中的 SQLite 主 `.db` 文件而遗漏 WAL 文件。备份、环境文件和访问链接均应限制访问权限。

### 4.4 同机部署与服务连接

控制端与 agent 可以运行在同一台 VPS 上：先完成控制端安装，再把这台机器添加到「服务器」，执行面板生成的 agent 安装命令。

1. **区分两个地址**：面板地址用于登录、管理和 agent 上报；「服务器 → 编辑 → 公网地址」用于客户端连接该服务器提供的服务。公网地址应填写直连公网 IP 或仅 DNS（灰云）域名。
2. **避免橙云地址误用**：普通 Cloudflare 橙云只转发受支持的 HTTP/HTTPS 流量，不能直接转发任意服务端口。面板使用橙云时，为服务使用独立的灰云域名或直连 IP。详见 [Cloudflare 端口说明](https://developers.cloudflare.com/fundamentals/reference/network-ports/)。
3. **检查端口**：服务端口不能与面板 HTTPS 入口等已有进程冲突。自动分配范围为 `20000–49999`；手动指定时也要确认端口空闲，并按服务使用的 TCP/UDP 类型放行主机防火墙与供应商安全组。agent 无需管理入站端口，不代表它部署的服务无需开放端口。
4. **分层排查超时**：先确认 agent 在线、配置版本已同步，再到「诊断」确认服务实例运行；随后核对客户端使用的地址、端口和公网连通性。配置下发成功、进程运行中，都不能单独证明客户端能够连接。
5. **地址修改后更新客户端**：保存服务器公网地址会更新其已部署节点的连接地址。客户端需刷新订阅；手动导入的节点需重新导入，旧链接中的地址不会自动改变。

### 4.5 卸载能力

发行附件中的独立卸载器支持控制端、agent 和两端同机的默认 systemd 安装，兼容早期 `v0.1.0`。命令与数据保留规则见 [卸载与清理](#7-卸载与清理)。

## 5. 更新

### 5.1 安装器部署的更新

执行以下命令，重新下载最新正式版的脚本并升级：

```bash
curl -fsSL https://github.com/YongshengWin/VpsCT/releases/latest/download/install.sh \
  | sudo bash -s -- --update
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

下面的命令**仅适用于本安装器生成的备份**。先进入 root 终端，把 `restore_backup` 改为实际备份目录；命令会保留失败现场，恢复后的数据以备份时间为准。

```bash
sudo -i
```

在 root 终端继续执行：

```bash
set -e
restore_backup=/opt/ctlvps/backups/pre-upgrade-替换为实际目录
for restore_file in data.tar.gz ctlvpsd.env ctlvpsd.service previous-release; do
  test -f "$restore_backup/$restore_file"
done
previous_release=$(cat "$restore_backup/previous-release")
test -x "$previous_release/ctlvpsd"
test -f "$previous_release/REPOSITORY"
tar -tzf "$restore_backup/data.tar.gz" >/dev/null
systemctl stop ctlvpsd
mv /opt/ctlvps/data "/opt/ctlvps/data.failed.$(date -u +%Y%m%dT%H%M%SZ)"
tar -xzf "$restore_backup/data.tar.gz" -C /opt/ctlvps
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

脚本先停止并禁用相应服务，再删除程序与服务定义。agent 卸载还会停止其部署的全部服务实例，并清理专用 nftables 表 `inet ctlvps`。服务停止失败时会中止文件删除。

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
| agent | `ctlvps-agent.service`、本项目的服务实例与模板、agent 和服务程序、专用 nftables 计量表 | `/var/lib/ctlvps-agent/`、`/var/log/ctlvps/`、`/etc/ctlvps/sing-box/`、旧版 `sing-box.json`、`/etc/ctlvps/snell/` |

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
3. **外部资源另行处理**：手动另存的备份、操作系统日志、独立维护任务结果与日志、DNS、安全组规则、面板内记录和其他 VPS 不会被删除。面板删除服务器记录也不会远程卸载 agent；应先卸载，再按需要删除记录，执行中的任务会阻止删除记录或重置注册令牌。
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
