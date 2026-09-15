#!/usr/bin/env python3
import os
import re

version = os.environ['RELEASE_VERSION']
repo = os.environ['RELEASE_REPOSITORY']
if not re.fullmatch(r'v\d+\.\d+\.\d+(?:-[A-Za-z0-9][A-Za-z0-9.-]*)?', version):
    raise SystemExit('invalid release version')
if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*', repo):
    raise SystemExit('invalid repository')
base = f'https://github.com/{repo}/releases/download/{version}'
print(f'''# VpsCT {version}

## 1. 安装控制端

本页命令安装 **{version}**。始终安装最新正式版的通用命令见 [README](https://github.com/{repo}#11-安装面板)。

支持 Debian / Ubuntu、Linux amd64 / arm64。以下默认安装方式会自动配置 HTTPS，需要域名解析正确，且 80/443 端口空闲、可从公网访问。

```bash
curl -fsSL {base}/install.sh | sudo bash -s -- --domain panel.example.com
```

已有 HTTPS 入口可使用下面的方式，无需为安装器腾出 80/443 端口。入口需自行配置证书并转发到本机 `127.0.0.1:8080`；使用其他 HTTPS 端口时，在站点地址中填写对应端口：

```bash
curl -fsSL {base}/install.sh | sudo bash -s -- --site-url https://panel.example.com --no-proxy
```

首次创建管理员前，在服务器上读取 `/opt/ctlvps/data/setup-token`；令牌不出现在公开日志中。

## 2. 升级

安装器管理的现有安装：

```bash
curl -fsSL {base}/install.sh | sudo bash -s -- --update
```

升级会短暂停止控制端并备份数据。该停服操作不会停止 VPS 上已运行的服务；新版 agent 分发文件可能触发各 VPS 的自动同步和重启。
完成本版安装后，可在「设置 → 系统 → 控制端维护」检查和升级后续版本。网页升级会在启动失败时尝试恢复旧程序和升级前数据。旧版首次启用网页维护需要先执行上面的终端升级。
手动安装与 Docker 部署请遵循 [运维说明](https://github.com/{repo}/blob/{version}/docs/operations.md)。

## 3. 卸载

下载此版本的卸载脚本，在要卸载的 VPS 上运行。预览控制端卸载范围：

```bash
curl -fsSL {base}/uninstall.sh | sudo bash -s -- --controller --dry-run
```

执行卸载时将 `--dry-run` 改为 `--yes`。只卸载 agent 使用 `--agent`，本机两端一起卸载使用 `--all`。默认保留配置与数据；加 `--purge` 会永久删除所选端的数据及默认目录内备份。自动生成的独占 Caddy 站点可加 `--purge --remove-caddy` 清理。详见 [卸载说明](https://github.com/{repo}/blob/{version}/docs/operations.md#7-卸载与清理)。

也可在「设置 → 系统」卸载控制端，或在服务器详情卸载 agent。操作需管理员密码、已启用的两步验证及目标名称确认；网页断开时通过目标服务器的维护日志核实最终结果。

## 4. 附件

每个控制端压缩包包含对应架构的控制端、两种架构的 agent、systemd 单元、卸载脚本和许可证。
`SHA256SUMS` 用于检查下载完整性，不替代发布者身份验证。

## 5. 发布前由维护者填写

- 本次变化与兼容性说明（参见 CHANGELOG.md）。
- 实际完成的 Linux 安装、HTTPS 和 agent 接入验证。
- 当前已知限制。
''')
