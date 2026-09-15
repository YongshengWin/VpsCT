# 参与贡献

[返回 README](README.md)

欢迎提交问题报告、文档改进和代码变更。产品名称和 GitHub 仓库名均为 **VpsCT**；二进制、数据目录和 systemd 单元沿用 `ctlvps` 命名。

## 1. 本地开发

需要 Go 1.26.8+、Node.js 24、npm；打发行包还需要 Python 3.11+。Linux 安装器面向 Debian / Ubuntu + systemd。

```bash
cd web
npm ci
cd ..
make dev-web
```

另一个终端运行 `make dev`。首次启动后，从本地 `data/setup-token` 获取初始化令牌，在页面创建管理员。请勿把令牌贴到 Issue 或终端录屏里。

## 2. 构建与检查

在项目根目录运行 `make build`，构建前端、本机控制端和双架构 Linux agent。已有前端依赖时，可运行 `make check` 做快速检查。

提交前运行完整检查：

```bash
bash scripts/check.sh
```

这会先构建前端，再运行 Go 静态检查、测试以及 Linux 双架构交叉编译。前端行为变化还应在浏览器验证。

安装器变更需要先构建测试附件，再运行带附件目录参数的集成测试：

```bash
make release VERSION=v0.0.0-ci REPOSITORY=example/VpsCT
bash scripts/test-install-container.sh release/v0.0.0-ci
```

这些附件仅用于测试，不作为公开发行版。构建和发布过程见 [发布流程](docs/releasing.md)。

## 3. 变更约定

- 一个 PR 聚焦一个问题，说明原来行为、修改后行为和验证方式。
- README 及链接的说明文档统一使用 `1.`、`1.1` 层级标题；修改结构时同时核对步骤顺序、适用环境、链接和标题锚点。
- 修改 API 字段时同步检查 `web/src/lib/types.ts` 与前端调用。
- 修改 agent 协议时保持旧 agent 的兼容性，并测试重试和失败场景。
- 数据库采用追加迁移；不要修改已发布版本执行过的迁移，也不要把真实数据库放进测试。
- 流量统一为 VPS 入站、出站、汇总；配额按汇总。模板与预设的业务边界见 [项目约定](AGENTS.md)。
- 依赖更新后重新生成第三方声明：`go mod download all`，`cd web && npm ci`，再在根目录运行 `python3 scripts/third-party.py`。
- 测试使用 `example.com`、保留测试域名和虚构数据。不要提交生产域名、节点密码、订阅令牌、SSH 地址或个人浏览记录。

## 4. 报告问题

普通问题使用 Issue 模板。安全漏洞使用 [SECURITY.md](SECURITY.md) 的私密渠道。分享日志前删掉请求 URL 中的订阅令牌、Cookie、Authorization、节点凭据和真实 IP。

提交贡献意味着你有权提供相应内容，并愿意按本仓库 LICENSE 授权你的贡献。引用第三方代码时保留原始版权与许可证说明。
