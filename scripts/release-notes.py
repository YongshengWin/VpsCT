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
手动安装与 Docker 部署请遵循 [运维说明](https://github.com/{repo}/blob/{version}/docs/operations.md)。

## 3. 附件

每个控制端压缩包包含对应架构的控制端、两种架构的 agent、systemd 单元和许可证。
`SHA256SUMS` 用于检查下载完整性，不替代发布者身份验证。

## 4. 发布前由维护者填写

- 本次变化与兼容性说明（参见 CHANGELOG.md）。
- 实际完成的 Linux 安装、HTTPS 和 agent 接入验证。
- 当前已知限制。
''')
