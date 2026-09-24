#!/usr/bin/env python3
import os
import re
from pathlib import Path

version = os.environ['RELEASE_VERSION']
repo = os.environ['RELEASE_REPOSITORY']
if not re.fullmatch(r'v\d+\.\d+\.\d+(?:-[A-Za-z0-9][A-Za-z0-9.-]*)?', version):
    raise SystemExit('invalid release version')
if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*', repo):
    raise SystemExit('invalid repository')
base = f'https://github.com/{repo}/releases/download/{version}'
changelog = Path('CHANGELOG.md').read_text()
sections = re.split(r'(?=^## \d+\. )', changelog, flags=re.M)
changes = next((s.strip() for s in sections if re.match(r'^## \d+\. ' + re.escape(version) + r'(?: —|\s|$)', s)), '')
if not changes:
    raise SystemExit('version missing from CHANGELOG.md')
changes = re.sub(r'^## \d+\.', '## 5.', changes, flags=re.M)
changes = re.sub(r'^### \d+\.', '### 5.', changes, flags=re.M)
# Release pages do not resolve repository-relative documentation links.
changes = re.sub(r'(\[[^\]]+\]\()((?:docs/|README\.md|CHANGELOG\.md)[^)]+)(\))',
                 lambda m: m[1] + f'https://github.com/{repo}/blob/{version}/' + m[2] + m[3], changes)

print(f'''# VpsCT {version}

## 1. 安装控制端

**本版不需要发布签名密钥、信任根或预置验证器。** 本页命令安装 **{version}**。始终安装最新正式版的通用命令见 [README](https://github.com/{repo}#11-安装面板)。

支持 Debian / Ubuntu、Linux amd64 / arm64。以下默认安装方式会自动配置 HTTPS，需要域名解析正确，且 80/443 端口空闲、可从公网访问。

```bash
curl -fLsS --proto '=https' --proto-redir '=https' {base}/install.sh -o install-vpsct.sh &&
sudo bash install-vpsct.sh --version {version} --domain panel.example.com
```

已有 HTTPS 入口可使用下面的方式，无需为安装器腾出 80/443 端口。入口需自行配置证书并转发到本机 `127.0.0.1:8080`；使用其他 HTTPS 端口时，在站点地址中填写对应端口：

```bash
curl -fLsS --proto '=https' --proto-redir '=https' {base}/install.sh -o install-vpsct.sh &&
sudo bash install-vpsct.sh --version {version} --site-url https://panel.example.com --no-proxy
```

首次创建管理员前，在服务器上读取 `/opt/ctlvps/data/setup-token`；令牌不出现在公开日志中。

## 2. 升级

安装器管理的现有安装：

```bash
curl -fLsS --proto '=https' --proto-redir '=https' {base}/install.sh -o install-vpsct.sh &&
sudo bash install-vpsct.sh --version {version} --update --auto-rollback
```

升级会短暂停止控制端并备份数据。该停服操作不会停止 VPS 上已运行的服务；新版 agent 分发文件可能触发各 VPS 的自动同步和重启。
完成本版安装后，可在「设置 → 系统 → 控制端维护」检查和升级后续版本。网页升级会在启动失败时尝试恢复旧程序和升级前数据。v0.1.1 可用上面的新版安装器命令升级，无需额外签名配置。默认采用 HTTPS 与 SHA256 校验，不提供独立发布签名或签名撤销保障。
手动安装与 Docker 部署请遵循 [运维说明](https://github.com/{repo}/blob/{version}/docs/operations.md)。

## 3. 卸载

下载此版本的卸载脚本，在要卸载的 VPS 上运行。预览控制端卸载范围：

```bash
sudo bash /opt/ctlvps/uninstall.sh --controller --dry-run
```

执行卸载时将 `--dry-run` 改为 `--yes`。只卸载 agent 使用 `--agent`，本机两端一起卸载使用 `--all`。默认保留配置与数据；加 `--purge` 会永久删除所选端的数据及默认目录内备份。自动生成的独占 Caddy 站点可加 `--purge --remove-caddy` 清理。详见 [卸载说明](https://github.com/{repo}/blob/{version}/docs/operations.md#7-卸载与清理)。

只有 agent 的 VPS 可使用 `/usr/local/libexec/ctlvps-agent-uninstall.sh --agent --dry-run`（需以 `sudo bash` 执行）。旧版没有此入口时，下载本版 `uninstall.sh` 后执行 `sudo bash uninstall.sh --agent --dry-run`，不需要控制端或重新注册。

也可在「设置 → 系统」卸载控制端，或在服务器详情卸载 agent。删除服务器默认同时卸载，成功后自动删除面板记录；失败或离线时保留记录。操作需管理员密码、已启用的两步验证及目标名称确认；网页断开时通过目标服务器的维护日志核实最终结果。

## 4. 附件

每个控制端压缩包包含对应架构的控制端、两种架构的 agent、systemd 单元、卸载脚本和许可证。
`SHA256SUMS` 用于检查下载完整性，不替代发布者身份验证。

{changes}
''')
