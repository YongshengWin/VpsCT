# 1. AI agent 接口

当前发行支持仅为 Linux AMD64（x86_64），适用于控制端、Docker 与被控 agent。ARM 不提供安装、升级或分发附件；待真实机器验证后再恢复。

VpsCT 使用开发者 API key，让 AI agent 通过现有 `/api/v1/` 业务接口管理产品功能。面板与 agent 共用权限、字段校验、审计及异步任务机制。仓库提供 [VpsCT skill](../skills/vpsct/SKILL.md)、标准库 [Python 客户端](../skills/vpsct/scripts/vpsct.py) 和 [常用请求示例](../skills/vpsct/references/api.md)。

这里的 AI agent 与 VPS 上的 `ctlvps-agent` 是两种客户端：前者使用开发者 key 调用管理接口；后者通过专用 `/api/agent/v1/` 协议主动连接控制端。开发者 key 不能作为 VPS enroll token 或 agent token 使用。

## 1.1 创建与撤销 key

在面板「头像菜单 → AI 能力」创建 key，普通用户和管理员都能访问该独立入口。填写名称、权限和可选有效期。创建需要当前账户密码；已启用两步验证时，还需要有效验证码。明文只在创建成功后显示一次，服务器只存储哈希。每个账户最多保留 32 个有效 key。

建议按 agent/用途分别发 key，先使用默认的只读权限；需要修改配置时选择读写组合，并设置合适的有效期。将明文交给秘密管理工具或注入进程环境，不写入仓库、配置示例、聊天或日志。

key 过期或撤销后不能继续调用。账户禁用/删除、密码或角色等安全状态变化会使旧 key 失效；原有 SSE 连接也会关闭。重新启用账户不能恢复已失效的 key。出现 401 时先确认这些状态，不能把旧 Cookie 作为降级认证方式。

管理接口只接受已登录的面板会话及 CSRF 验证，API key 无法发新 key 或撤销自己/其他 key：

| 操作 | 方法与路径 | 响应 |
|---|---|---|
| 列出本账户 key | `GET /api/v1/api-keys` | `{"list":[...]}`，仅元数据，不含明文 |
| 创建 | `POST /api/v1/api-keys` | 201，`{"api_key":{...},"key":"..."}`，明文仅此一次 |
| 撤销 | `DELETE /api/v1/api-keys/{id}` | 204；只能撤销本账户 key，重复撤销同一记录幂等 |

创建 body 字段为 `name`（1–80 字，不能含控制字符）、`scopes`（`["read"]`、`["write"]` 或 `["read","write"]`）、`expires_at`（可省略；RFC 3339 且晚于当前时间）、`password`、`code`。省略 expires_at 表示不过期。密码和验证码只用于当次二次认证。

## 1.2 认证与权限

业务请求只需 `Authorization: Bearer <API_KEY>`，不需要 Cookie、浏览器登录或 CSRF token。仅使用最终的 HTTPS 面板 origin；不要跟随重定向或关闭证书验证。开发测试可显式允许回环 HTTP，不允许公网明文 HTTP。

| scope | 能调用的业务方法 | 限制 |
|---|---|---|
| `read` | GET、HEAD | 仍需满足账户角色和资源归属 |
| `write` | POST、PUT、DELETE 等写方法 | 不隐含 read；POST preview 也属于 write |
| `read` + `write` | 读与写 | 常见“读取 → 预览 → 修改 → 验证”任务需要两者 |

key 继承签发账户的角色。成员只能读取其有权访问的分享/订阅及成员接口，不能因持有 write scope 获得管理员权限。管理员接口仍要求管理员角色，资源归属等约束仍由原业务处理器验证。

**只读不等于秘密脱敏。** 授权范围内的节点密码/URI、订阅 token/访问链接、外部订阅 URL、服务器地址和渲染配置可能出现在 GET 响应中。只将结果交给有权限的用户，agent 日常回复应提取状态、计数、用量等必要字段。完整响应存放在权限受限的临时文件，用后清理，不写入仓库或发送到不相关服务。

API key 无法调用登录/初始化接口，不能获取 CSRF、修改自己或他人的两步验证设置、管理 API keys。资料、头像、密码及其他业务接口按原有权限和所需 scope 工作；改密码会令当前 key 失效。控制端/agent 升级和卸载依旧要求管理员密码、已启用的两步验证及实际目标确认，key 不替代这些条件。

每个 key 和来源 IP 各有每分钟 240 次请求限制；请求体一般最多 4 MiB，认证与维护等接口有更小限制。响应提供 `X-Request-ID` 供排查，请勿用它代替任务编号。

## 1.3 动态接口目录

先用 key 调用以下发现接口，按实际部署版本及权限选择操作：

| 方法与路径 | 内容 |
|---|---|
| `GET /api/v1/agent/capabilities` | 产品/版本、认证角色与 scopes、请求限制、操作目录、workflows 及部署入口说明 |
| `GET /api/v1/agent/openapi.json` | OpenAPI 3.1 路由、认证、安全边界、路径/查询参数及提取的请求字段类型 |
| `GET /api/v1/agent/skill.zip` | 完整 skill 包，包含 `vpsct/SKILL.md`、`vpsct/scripts/vpsct.py`、`vpsct/references/api.md`；不含用户数据 |
| `GET /api/v1/agent/skill` | 与部署版本一致的 SKILL.md |
| `GET /api/v1/agent/client.py` | 同版本的标准库 Python HTTP 客户端 |
| `GET /api/v1/agent/reference` | 常用请求示例，可保存为 skill 的 `references/api.md` |

首次安装无需 key，可下载公开的 `GET /agent/skill/install.py`。该脚本与控制端版本一致，内含完整 skill ZIP 的 base64 和 SHA256，不含面板地址、API key 或用户数据；此路径是安装入口，不属于 `/api/v1/` 业务接口目录。

`workflows` 给出接入 VPS、部署/导入节点、两台或三台线路、停止清理、已有节点出口、订阅、分享、监控和维护的步骤、完成条件和恢复语义。每一步引用实际业务 method/path/operation_id，并按当前 key 的 scopes/角色返回 available。流程说明不会自动授予主机访问或写入权限；资源校验仍在执行时进行。

`deployment` 标明原生 systemd 与 Docker Compose 的部署方式、主机授权要求、初始化令牌和会话签发 key 的边界，并指向同版本 reference 第 2 章。控制端尚未运行时可从可信仓库内的 `skills/vpsct/` 使用同一指引，无需先假定存在可用 API。Docker 运维使用宿主机脚本，不能调用原生维护执行器。

目录从现有受认证路由自动登记。`available`/`x-available` 表示当前身份的调用权限，不保证目标资源存在、资源归属正确或对应 VPS 已具备运行能力。请求字段类型从处理器提取；业务必填组合、字段范围、协议能力及响应语义仍须遵循业务校验和请求示例，不能把未列出的约束当作不存在。

设置写入目录列出当前可编辑键，包含尚未配置的可选项；出口的 `config` 按 `kind` 提供 direct、SOCKS5、SS-2022、SSH、WireGuard 的字段类型分支。这些描述只包含产品结构，不带用户配置或凭据值。

转发线路支持 `GET /api/v1/access-routes/{op}` 按稳定编号查询，不必扫描最多 256 条的列表。预览/创建合约提供两台/三台结构、端口自动分配、来源模式和影响凭证说明；这些操作的响应提供 stage、message、revision 或 candidate/token/checks 的结构。UI 默认三台；API 的 `ix_server_id=0` 或省略明确表示无额外中转。完整请求与超时恢复示例见 [线路指引](../skills/vpsct/references/api.md#18-转发线路两台三台及导入落地)。

目录覆盖服务器、节点、外部订阅、订阅、模板、预设、分享、流量、网络/出口/转发/中转、连接日志、账户、设置、审计、封禁、系统状态与维护。会话专用接口会标记 `session_only=true`；发现并不授权绕过该限制。公开订阅与 VPS 专用通信接口使用各自凭据，不接收开发者 key。

## 1.4 配合 skill 使用

在面板「头像菜单 → AI 能力」选择 Codex 或 Claude Code，复制网络一键安装命令，在运行 AI 客户端的本机终端执行。需要 macOS/Linux 和 Python 3.9+；使用当前用户执行，不需要 sudo 或 API key。命令先将同版本安装器完整下载到临时文件，下载成功后才执行，不跟随重定向；公网入口要求 HTTPS，回环 HTTP 仅用于本机开发。

安装器不再联网，先检查内嵌 ZIP 的 SHA256、大小、文件名白名单及链接类型，再在目标父目录暂存完整文件，通过目录 rename 安装。完整包只有 SKILL.md、scripts 和 references 三个产品文件，不写入 key 或用户配置。默认客户端为 Codex：

| 参数 | 目标 skill 父目录 |
|---|---|
| `--client codex` | `$CODEX_HOME/skills`，未配置时为 `~/.codex/skills` |
| `--client claude` | `~/.claude/skills` |
| `--dir /your/skill-directory` | 使用指定父目录，最终安装为其下的 `vpsct/` |

同版本重复安装不会修改已有文件。遇到不同版本或修改过的完整 VpsCT skill 时拒绝覆盖，需核对后显式加 `--update`；不覆盖任意目录、非产品文件或符号链接。升级失败会恢复旧目录；若恢复也失败，错误会给出保留的完整旧目录位置，按该位置恢复后再试。自定义资源应先手动备份/移出或选择新的 `--dir`。完成安装后重新加载客户端的技能，再配置业务 API 环境变量。

也可在同一页面下载完整 VpsCT skill ZIP，手动将其中的 `vpsct/` 目录解压到 skill 父目录；例如默认 Codex 安装后为 `~/.codex/skills/vpsct/SKILL.md`。

也可将仓库 `skills/vpsct/` 整个目录交给 agent 的 skill 系统。自动化下载使用 `GET /api/v1/agent/skill.zip` 并保存为 ZIP；单文件入口保留供检查，下载单独的 SKILL.md 时，需同时保存 `/agent/client.py` 到 `scripts/vpsct.py`、`/agent/reference` 到 `references/api.md`，保留相同目录结构。

通过环境/秘密管理工具提供：

```text
VPSCT_URL=https://panel.example.com
VPSCT_API_KEY=<由秘密管理工具注入，不粘贴到命令参数>
```

已具备客户端后，从仓库根目录调用发现与查询：

```bash
python3 skills/vpsct/scripts/vpsct.py GET /api/v1/agent/capabilities
python3 skills/vpsct/scripts/vpsct.py GET /api/v1/agent/openapi.json
python3 skills/vpsct/scripts/vpsct.py GET '/api/v1/servers/1/traffic?days=7'
```

创建/修改用 JSON 文件或 stdin 传 body，避免把敏感字段放在命令行。节点、订阅等响应使用 `--output`，在仓库外的私有临时目录保存：

```bash
python3 skills/vpsct/scripts/vpsct.py POST /api/v1/subscriptions/preview \
  --body request.json --output /private/tmp/vpsct-preview-response.json
```

上面的请求字段见 [订阅预览示例](../skills/vpsct/references/api.md#13-订阅预览与创建)。更适合持续自动化的方式是由 agent 建立权限为 0700 的临时目录，body 和输出均放入其中，避免共享临时目录出现可预测文件名。

客户端支持 GET、POST、PUT、DELETE；`--body -` 从 stdin 读取，`--timeout` 设置连接/读超时，默认 30 秒。它验证 JSON、拒绝所有重定向、忽略环境代理、不保存 Cookie、不自动重试。输出文件在请求前以 0600 新建，不覆盖已有文件；无法新建时不发送请求，网络失败时可能留下空文件。错误退出码非零。开发环境 `--allow-http-localhost` 只允许 localhost 或回环 IP 的 HTTP。

SSE `/api/v1/events` 使用客户端的有界流式模式，按完整事件即时输出 NDJSON，每行是 `{"event":"...","data":...}`：

```bash
python3 skills/vpsct/scripts/vpsct.py GET /api/v1/events \
  --stream --max-events 20 --timeout 30
```

初始 `hello` 在服务端完成事件订阅后发送，可作为本次连接已开始监听的确认；它不代表业务资源已应用，断线期间的事件不补发。

默认最多 20 个事件，包含初始 `hello`，忽略 ping 注释；`--timeout` 在流式模式下限制整个连接的总期限。单事件最多 1 MiB，整条流最多 16 MiB；到达事件数返回成功，超时/断连/截断返回非零，已经收到的完整事件保留。只支持 GET、不能带 body、不能同时用 `--output`，不自动重连。事件 data 可能含业务秘密，脚本只遮盖认证 key；只在用户授权的本机终端处理，不将完整流回显到聊天或无关服务。头像、导出等二进制响应需要 `--output`。

连接日志 CSV 为 `GET /api/v1/connlog/export`，可用 `from`、`to`（RFC 3339 或 `YYYY-MM-DD`）缩小时间范围。导出内部使用固定批量和固有行数上限，会忽略传入的 `limit`/`offset`；不能通过 offset 分页拼出完整 CSV。客户端完整响应上限为 16 MiB，大导出应缩短时间范围，不能将超限或受行数上限约束的结果宣称为完整导出。

## 1.5 变更、结果确认与错误

读取目标后构造符合原接口的请求，PUT 不应默认视作 PATCH；服务器/分享等接口需保留不相关字段。`PUT /api/v1/settings` 是合并更新，只提交本次修改且允许写入的键，不能回传整份 GET 的内部键和密钥掩码。节点 `params`、出口 `config`、订阅 `rule_providers` 等动态 JSON 的内部字段仍须有对应产品 schema 或已有配置依据，通用 object 类型不代表字段不限。网络、出口、中转、转发等提供 preview 的操作，应先取得实际影响与确认凭证，遵守版本/能力校验。

保存配置、201 创建或 202 接受任务后，继续查询应用状态。服务器 desired/agent 的 revision 和 apply 状态、网络 operation 的状态、维护 job 的状态才说明执行进展。面板保存成功和 VPS 应用成功是两个阶段，agent 离线或失败时应如实报告。

错误一般为 `{"error":{"code":"...","message":"..."}}`，可结合 HTTP 状态和 X-Request-ID 排查：

| 状态 | agent 应采取的动作 |
|---|---|
| 401 | key 缺失/无效/过期/撤销或账户安全状态变化，停止依赖它的操作 |
| 403 | 检查 scopes、角色、资源权限或二次认证；不能自动更换更高权限身份 |
| 409 / 428 | 状态、版本或网络影响已变化，重新读取/预览后再决定 |
| 429 | 遵循 `Retry-After`，限制并发与轮询频率 |
| 5xx / 超时 / 断连 | 结果可能未知；先查询目标及任务状态，不能盲目再次写入 |

普通业务写操作没有统一的幂等保证；创建节点/分享、轮换凭据等不得自动重试。网络操作和维护有各自 operation/id 与版本检查，按该接口核对原任务后再处理。查看维护能力和任务字段见 [网络变更与维护](../skills/vpsct/references/api.md#15-网络变更与维护)。

## 1.6 功能覆盖与执行边界

已将前端功能调用逐项对照受认证业务路由与能力目录。以下业务模块均复用原接口，管理员持有 read/write key 时可调用；成员仍按原资源归属和角色范围访问。部署版本的实时 `/agent/capabilities` 和 OpenAPI 才是当前可调用范围，表格不代替逐资源校验。

| 业务模块 | AI 可执行内容 | 角色与额外条件 |
|---|---|---|
| 服务器、节点 | 状态/用量/能力查询，添加/修改记录，部署/导入/链式节点，凭据轮换，配置下发 | 管理员；部署依赖 VPS 的协议/内核能力，保存后继续核对应用状态 |
| 订阅、模板、预设、分享 | 查询、预览、生成/转换外部订阅，维护模板/预设，创建/暂停/恢复/撤销分享 | 成员可读取授权资源；创建/修改要求管理员；POST 预览需要 write，响应可含秘密 |
| 网络、出口、转发、中转 | 能力/拓扑查询，预览影响，创建/更新/退出操作，查询进展及受控重试 | 管理员；遵守 preview、确认凭证、版本和官方内核能力约束 |
| 流量、日志、审计 | 查询方向/账期/配额，校准、计费配置，日志查询/导出、审计及封禁管理 | 管理员管理全局；成员仅访问其授权分享的用量/事件；日志与导出按原隐私设置工作 |
| 账户、用户、设置 | 读取本人资料、更新昵称/头像/密码，管理员管理账户及产品设置 | 成员限本人资料；用户/设置管理要求管理员；修改密码令旧 key 失效；两步验证管理需面板会话 |
| 控制端与 VPS agent 维护 | 查看能力/版本/任务，提交升级或卸载，核对结果 | 管理员、密码及已启用的两步验证码；卸载需目标确认；控制端需维护 worker，VPS 需在线且支持维护的 agent |
| 实时事件 | 使用有界 SSE 监听产品事件，逐事件读取进展 | 成员与管理员按原权限；read scope，流式客户端，不自动重连 |

首次管理员初始化、登录、创建/撤销 API key 及两步验证管理使用面板会话，属于接入和安全边界。VPS 首次安装与旧安装的信任迁移可由 API 生成命令；执行仍需要用户授权该目标操作系统上的操作，不会因为读取命令就自动获准 SSH。离线 VPS 可以保存部分配置，但只能报告待应用，不能宣称部署已完成。

公开订阅 `/s`、短链接 `/r` 与 VPS agent 专用协议使用各自凭据。已移除的配置上传与托管功能不在支持范围，旧记录不恢复下载。

## 1.7 本地验证与后续扩展

新增功能的 agent 友好要求已写入 `AGENTS.md`：业务 API、权限、请求合约、状态/完成判定、恢复方式与 Skill 示例同步交付；关键新写流程需测试无 Cookie 的 Bearer 调用。

业务路由通过统一注册入口进入能力目录。修改处理器请求类型或查询参数后，从仓库根目录运行 `go generate ./internal/api` 更新内嵌字段合约；`make check` 和 `scripts/check.sh` 会检查合约是否过期，并验证客户端安全行为。前端改动仍需先 typecheck/build，再构建带内嵌页面的控制端。

客户端安全测试只启动 127.0.0.1 临时测试服务器，使用虚构凭据，不连接真实面板或 VPS：

```bash
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover \
  -s skills/vpsct/scripts -p 'test_*.py' -v
```

安装器安全和行为测试仅使用隔离临时目录，不接触用户的真实 skill 目录，也不联网：

```bash
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover \
  -s skills -p 'test_install_vpsct.py' -v
```

接口发现与 skill 复用现有业务能力，API keys、登录和两步设置等会话专用操作仍遵守前述安全边界。下一步可按实际自动化需求增加更细的资源 scopes、统一幂等 key、稳定的全量响应 schema 和 MCP 适配；这些扩展需复用现有权限/任务机制。目前不提供这些额外协议或权限层，不能让 agent 假定它们已可用。

UDP 容量自动化须区分准入上限和已验证负载：8192 是整机启用配置预算上限，默认 256 MiB 代理预算的发布负载基线为 1024 个持续活跃会话。不要把配置通过或 202 接受当成 8192 会话容量认证；必须核对应用状态与 UDP 健康诊断，发生保护性暂停时应降低负载并查询错误。
