# 安装、升级与恢复

[返回 README](../README.md)

本文按“选择部署方式 → 完成安装 → 配置与运维 → 更新 → 失败恢复”的顺序组织。除明确标注在被管理 VPS 上执行的步骤外，命令都在控制端服务器运行。

## 1. 选择部署方式

| 方式 | 适用场景 | 后续维护方式 |
|---|---|---|
| 安装器部署 | 新装面板，希望自动配置系统服务 | 使用目标版本安装脚本的 `--update` |
| 手动二进制部署 | 已有安装布局，或希望自行管理服务配置 | 备份后手动替换程序 |
| Docker 部署 | 已使用容器管理服务 | 更新源码、重建镜像并保留数据卷 |

三种方式的升级和恢复步骤不同。安装器会拒绝覆盖现有手动安装或未知数据目录。

以下示例使用官方仓库 `YongshengWin/VpsCT` 和版本 `v0.1.0`。安装其他版本时，请使用对应 [Release](https://github.com/YongshengWin/VpsCT/releases) 提供的命令。

## 2. 使用安装器部署

### 2.1 检查环境并下载脚本

安装器要求 Debian / Ubuntu、正在运行的 systemd、root 权限，以及 Linux amd64 / arm64。服务器需能访问 GitHub Release 和系统软件源；首次安装要求本机 8080 端口空闲。

先从目标版本下载安装脚本，再选择下面一种 HTTPS 配置方式：

```bash
curl -fL https://github.com/YongshengWin/VpsCT/releases/download/v0.1.0/install.sh -o install.sh
```

Release 附件中的脚本已写入仓库和版本。若使用源码目录中的脚本，需额外传入 `--repo YongshengWin/VpsCT --version v0.1.0`。

### 2.2 自动配置 HTTPS

先把域名解析到控制端服务器，确保 80、443 端口空闲且公网可访问，然后执行：

```bash
sudo bash install.sh --domain panel.example.com
```

安装器配置系统服务，并安装 Caddy 提供 HTTPS。已有 Caddy 配置或 80、443 端口被占用时，改用下一节的方式；安装器不会覆盖已有站点或自动修改防火墙。

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
| `/opt/ctlvps/backups/pre-upgrade-*/` | 每次升级前的数据、配置、服务定义和旧版本路径 |

已下载完整发行附件时，可增加 `--assets-dir /path/to/release --version v0.1.0`。该目录需包含对应架构的压缩包和 `SHA256SUMS`；安装仍会访问系统软件源准备依赖。

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

## 5. 更新

### 5.1 安装器部署的更新

先按第 2.1 节下载目标版本的安装脚本，再执行：

```bash
sudo bash install.sh --update
```

升级保留站点配置，不同时传入域名或 HTTPS 模式参数。使用源码中的脚本时，可指定仓库和目标版本；`--version latest` 会在开始时解析一次最新正式 Release。

安装器先校验发行包，随后停止控制端、备份完整数据、切换版本并检查健康状态。账户、配置和分享不会重新初始化。它不支持直接降级；需要恢复旧版时使用对应版本的数据备份。

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
| 重新下发配置 | 让该 VPS 重新应用当前服务配置 |

网页当前没有升级控制端自身的按钮。服务内核版本需在设置中选中并保存后才会改变。控制端短暂停服本身不会停止 VPS 上已运行的服务；agent 更新或重新应用配置可能引起服务调整。

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

## 7. 停用与清理

systemd 部署可用 `sudo systemctl disable --now ctlvpsd` 停用面板；Docker 部署可在 `deploy/` 目录运行 `docker compose stop ctlvpsd`。

停用控制端会中断管理与状态上报，VPS 上的 agent 和已有服务仍需单独管理。清理前分别确认控制端数据、备份、HTTPS 配置以及 VPS 上的状态文件；没有隐式删除全部数据的一键卸载命令。
