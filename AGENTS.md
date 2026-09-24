# VpsCT

给人看的面板和 GitHub 仓库均叫 **VpsCT**。本地工作目录、二进制、数据目录、systemd 沿用 **ctlvps** 命名：控制端 `ctlvpsd`，VPS 上 `ctlvps-agent`。

一句话：一台控制端管很多台 VPS。在面板里加服务器、部署节点、做订阅和分享。回复用**简体中文**。

## 1. 两块怎么装

**控制端支持 Release 安装器。** 根目录 `install.sh` 安装预编译控制端、双架构 agent 分发文件和 systemd，可选 Caddy HTTPS；`--update` 停服备份后升级。源码构建用 `make build`，发行附件用 `make release VERSION=vX.Y.Z REPOSITORY=OWNER/VpsCT`，其中 OWNER 是实际 GitHub 用户或组织。也可 `cd deploy && docker compose up -d --build`。第一次创建管理员必须使用数据目录的 `setup-token`，不能重新开放无令牌初始化。

**被控 VPS 使用独立的一键脚本。** 面板「服务器 → 添加 → 生成安装命令」，在那台机器上 root 执行。脚本会 enroll、装 systemd。agent **只出不进**，VPS 不用开管理端口。节点不是再装一次，是面板里点部署，agent 自己收敛 sing-box / snell。

细节见 `README.md`、`docs/operations.md` 和 `docs/releasing.md`。

**卸载使用独立 `uninstall.sh`。** `--controller` / `--agent` / `--all` 三选一，默认保留数据；`--purge` 才删除数据。先用 `--dry-run` 看范围。`--remove-caddy` 需要控制端加 `--purge`，只接受安装器生成的独占配置。两端共用父目录，不可直接删整个 `/opt/ctlvps` 或 `/etc/ctlvps`。2026-09-15 更新的 v0.1.0 附件包含卸载器和网页维护；早期安装需先终端更新一次以启用网页维护。

## 2. 代码在哪

网页维护由 `internal/maintenance` 的固定 Unix socket 服务和独立 systemd worker 执行。控制端仍不以 root 运行。维护任务要持久化、幂等、二次认证、按端隔离；不要让网页传入任意命令、路径或下载仓库。升级/卸载验证使用 `scripts/test-maintenance-container.sh` 的隔离环境，不直接操作真实 VPS。

| 路径 | 干什么 |
|---|---|
| `cmd/ctlvpsd` | 控制端：API + 内嵌前端 |
| `cmd/ctlvps-agent` | VPS 端 |
| `web/` | React 面板，build 进 `web/dist`，再打进 `ctlvpsd` |
| `internal/api` | REST / SSE / 公开订阅 `/s` `/r` |
| `internal/subscription` | 模板、预设、各客户端格式 |
| `internal/core` | 内核安装与配置 |
| `deploy/` | systemd、compose |

前端改完要 `cd web && npm run typecheck && npm run build`，再编 Linux `ctlvpsd` 才进生产二进制。

本地：一个终端 `make dev-web`，一个 `make dev`。验证：`make check` 或 `bash scripts/check.sh`。

## 3. 容易搞混

- 订阅只从节点库生成或转换外部订阅，分享订阅由分享管理。已删除配置上传与托管功能，不要重新加入；旧上传记录保留数据但不再提供下载。
- **模板**是客户端打开订阅时的整份底稿（DNS、组、分流）。**预设**只是往订阅的「代理组 / 规则」里盖一层，客户端下载不到。两栏空着就听模板。套预设要组和规则一起套，只套一半会打架。分享没有这两栏，套预设对分享无效。一种模板只覆盖一种格式。
- **流量**：入站 = 网卡收，出站 = 网卡发，汇总 = 入+出。配额按汇总。不要再搞「单双向」，不要对分享做 `2×`，不要把 Snell IPAccounting 和 nft 加在一起。
- **重置日**：1–28 固定那天；29/30/31 只能是「每月最后一天」（存 31）。
- 内核版本钉死在设置里，下拉从上游拉列表，**选中保存才升级**，不会自动追最新。
- sing-box 只适配官方发行版；不修改或自行编译内核，不保留自编译版本、补丁和含其二进制的发行产物。官方版未通过验证的能力应明确限制，不能以维护自有内核分支解决。

## 4. 别做的事

- 不要把节点密码、订阅 token、SSH 主机 IP、用户桌面里的订阅地址写进仓库或回复。
- 不要把个人域名、`update-url`、个人 IP-CIDR 写进产品模板。
- 不要擅自 commit / push 或发布 Release；用户说了再做。
- 第三方许可文本由 `scripts/third-party.py` 生成；依赖变更要同步更新。
- README 及链接的说明文档统一使用 `1.`、`1.1` 层级标题；同时检查步骤、部署方式和链接，保持原始许可证正文完整。
- 在线 GeoIP 默认关闭，启用会向第三方发送公网客户端 IP，不能静默开启。
- 不要改 git config，不要 `--no-verify`。
- 前端改了可见行为，能开浏览器就点一遍；不能开就说清楚验证了什么。
