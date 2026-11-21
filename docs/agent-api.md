# 1. AI agent 接口

当前发行支持仅为 Linux AMD64（x86_64），适用于控制端、Docker 与被控 agent。ARM 不提供安装、升级或分发附件；待真实机器验证后再恢复。

VpsCT 使用开发者 API key，让 AI agent 通过现有 `/api/v1/` 业务接口管理产品功能。面板与 agent 共用权限、字段校验、审计及异步任务机制。仓库提供 [VpsCT skill](../skills/vpsct/SKILL.md)、标准库 [Python 客户端](../skills/vpsct/scripts/vpsct.py) 和 [常用请求示例](../skills/vpsct/references/api.md)。

这里的 AI agent 与 VPS 上的 `ctlvps-agent` 是两种客户端：前者使用开发者 key 调用管理接口；后者通过专用 `/api/agent/v1/` 协议主动连接控制端。开发者 key 不能作为 VPS enroll token 或 agent token 使用。

## 1.1 创建与撤销 key

管理员和被授予 ai.access 权限的普通用户可在面板「头像菜单 → AI 能力」创建 key。普通用户默认没有该权限；撤销后其 key 无法认证。填写名称、权限和可选有效期。创建需要当前账户密码；已启用两步验证时，还需要有效验证码。明文只在创建成功后显示一次，服务器只存储哈希。每个账户最多保留 32 个有效 key。

建议按 agent/用途分别发 key，先使用默认的只读权限；需要修改配置时选择读写组合，并设置合适的有效期。将明文交给秘密管理工具或注入进程环境，不写入仓库、配置示例、聊天或日志。

key 过期或撤销后不能继续调用。账户禁用/删除、密码或角色等安全状态变化会使旧 key 失效；原有 SSE 连接也会关闭。重新启用账户不能恢复已失效的 key。出现 401 时先确认这些状态，不能把旧 Cookie 作为降级认证方式。

以下 API key 管理接口需要已登录的面板会话。写操作还需通过 CSRF 验证。

| 操作 | 方法与路径 | 响应 |
|---|---|---|
| 列出本账户 key | `GET /api/v1/api-keys` | `{"list":[...]}`，仅元数据，不含明文 |
| 创建 | `POST /api/v1/api-keys` | 201，`{"api_key":{...},"key":"..."}`，明文仅此一次 |
| 调整功能 | `PUT /api/v1/api-keys/{id}/operations` | 200，保存本账户 key 的功能范围，需密码和已启用的两步验证码 |
| 撤销 | `DELETE /api/v1/api-keys/{id}` | 204；只能撤销本账户 key，重复撤销同一记录幂等 |

创建 body 字段为 `name`（1–80 字，不能含控制字符）、`scopes`（`["read"]`、`["write"]` 或 `["read","write"]`）、`expires_at`（可省略；RFC 3339 且晚于当前时间）、`operations`（可省略或为 null 表示全部获授权功能；数组为能力目录的 operation_id 白名单）、`password`、`code`。省略 expires_at 表示不过期。密码和验证码只用于当次二次认证。调整功能请求为 `{"operations":["..."],"password":"...","code":"..."}`；`operations: []` 只保留发现接口。每个 ID 必须是当前账户可调用的非会话专用操作，最多 512 项。调整立即生效并记录审计；原有 key 的默认值为 null。选择全部功能时，未来账户获授的新操作也可能可用。

## 1.2 认证与权限

业务请求只需 `Authorization: Bearer <API_KEY>`，不需要 Cookie、浏览器登录或 CSRF token。仅使用最终的 HTTPS 面板 origin；不要跟随重定向或关闭证书验证。开发测试可显式允许回环 HTTP，不允许公网明文 HTTP。

| scope | 能调用的业务方法 | 限制 |
|---|---|---|
| `read` | GET、HEAD | 仍需满足账户角色和资源归属 |
| `write` | POST、PUT、DELETE 等写方法 | 不隐含 read；POST preview 也属于 write |
| `read` + `write` | 读与写 | 常见“读取 → 预览 → 修改 → 验证”任务需要两者 |

普通用户的 AI 接入仅涵盖其分享、分享中的个人模板及另行明确授权的管理操作；默认不会开放服务器、独立订阅、用户管理等页面外的功能。每项操作必须同时满足账户权限、资源归属、read/write scope 和该 key 的功能白名单。能力发现接口不受白名单限制，但仍需要 read scope；它返回当前 key 的实时可用操作及工作流步骤。key 继承签发账户的角色。例如，获授 `templates.edit` 的成员可用具备 `write` scope 且允许该操作的 key 编辑公共模板。

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

`workflows` 给出接入 VPS、部署/导入节点、两台或三台线路、停止清理、已有节点出口、订阅、分享、监控和维护的步骤、完成条件和恢复语义。每一步引用实际业务 method/path/operation_id，并按当前 key 的 scopes、功能白名单及账户权限返回 available。流程说明不会自动授予主机访问或写入权限；资源校验仍在执行时进行。

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

已将前端功能调用逐项对照受认证业务路由与能力目录。以下业务模块均复用原接口，管理员持有 read/write key 时可调用；被授予 ai.access 的普通用户也可通过 key 按原资源归属和产品权限访问。部署版本的实时 `/agent/capabilities` 和 OpenAPI 才是当前可调用范围，表格不代替逐资源校验。

| 业务模块 | AI 可执行内容 | 角色与额外条件 |
|---|---|---|
| 服务器、节点 | 状态/用量/能力查询，添加/修改记录，部署/导入/链式节点，凭据轮换，配置下发 | 管理员；部署依赖 VPS 的协议/内核能力，保存后继续核对应用状态 |
| 订阅、模板、预设、分享 | 查询、预览、生成/转换外部订阅，维护模板/预设，创建/暂停/恢复/撤销分享 | 成员可为自己的分享管理个人模板，按委派权限管理公共模板或指定分享。独立订阅管理仍需管理员。POST 预览需要 write，响应可含凭据 |
| 网络、出口、转发、中转 | 能力/拓扑查询，预览影响，创建/更新/退出操作，查询进展及受控重试 | 管理员；遵守 preview、确认凭证、版本和官方内核能力约束 |
| 流量、日志、审计 | 查询方向/账期/配额，校准、计费配置，日志查询/导出、审计及封禁管理 | 管理员管理全局；成员仅访问其授权分享的用量/事件；日志与导出按原隐私设置工作 |
| 账户、用户、设置 | 读取本人资料、更新昵称/头像/密码，管理员管理账户及产品设置 | 成员限本人资料；用户/设置管理要求管理员；修改密码令旧 key 失效；两步验证管理需面板会话 |
| 控制端与 VPS agent 维护 | 查看能力/版本/任务，提交升级或卸载，核对结果 | 管理员、密码及已启用的两步验证码；卸载需目标确认；控制端需维护 worker，VPS 需在线且支持维护的 agent |
| 实时事件 | 使用有界 SSE 监听产品事件，逐事件读取进展 | 成员与管理员按原权限；read scope，流式客户端，不自动重连 |

公共模板的创建、编辑和删除可以分别授权给成员；目标账户的 `/api/v1/me/permissions` 可查询有效授权，操作目录的 `required_permission` 和 `available` 反映当前权限。AI 接入由 `ai.access` 独立控制，默认不授予普通用户，不能因此获得其他业务权限。Skill 每次任务应使用当前 key 重新读取能力清单和工作流的可用步骤；资源范围仍由实际接口校验。授权与撤销需要当前账户密码及已启用的两步验证码；权限变化令受影响账户和下级授权链的会话及 API key 失效。拥有 `write` scope 不会授予未授权操作。其他管理模块仍遵循现有管理员边界。

分享响应中的 `public_id` 是随机生成、持久化的页面标识，可用 `GET /api/v1/share-pages/{public_id}` 查询；该接口仍要求登录及分享归属或委派权限，标识本身不是凭证。原有数字 ID 和接口继续用于 API 操作；网页不再接受数字分享地址。分享转交后标识不变，但访问权限随归属变化。

Mihomo 模板直接编辑 YAML 正文。个人用户可用 `POST /api/v1/shares/{id}/personal-templates/preview-draft` 提交草稿 `content`，仅针对本人分享生成预览；管理员可用 `POST /api/v1/shares/{id}/template-preview-draft` 按指定分享预览。响应 `groups` 和 `nodes` 展示组成员及地区来源，`warnings` 指出常见规则引用缺失，`body` 可能含节点凭据。草稿预览不保存模板，POST 需要 write scope。保存后应再次预览已保存版本并应用，按选择接口核对结果。管理员可以用 `PUT /api/v1/nodes/{id}/exit-region` 设置或清空经核实的实际出口地区（`exit_region` 为两位代码或空字符串），再读取节点核对；该字段只影响模板分组。

`GET /api/v1/nodes/{id}` 返回节点当前网络版本和保存的 `network`。修改监听、出口或入口来源时，先提交完整候选到 `POST /api/v1/nodes/{id}/network/preview`，再用 `PUT /api/v1/nodes/{id}/network` 提交预览返回的完整 `network`、稳定的 `operation_id` 和节点当前的 `expected_revision`，并将 `expected_impact` 设为预览的 `impact.token`。地区拒绝模式的 `network` 含固定的 `geo_provider`、`geo_version` 和 IPv4/IPv6 地址段。不要只提交国家代码后跳过预览；保存成功只表示请求进入队列。

创建或更新服务器时，新增或修改的 `public_host_ipv6` 会自动核对格式、DNS 及近期可用的网卡清单。校验失败时本次表单内容不会保存。没有近期网卡清单时可先保存，部署时再验证归属。已有服务器可使用 `GET /api/v1/servers/{id}/ipv6-endpoint/check?host=v6.example.net` 单独诊断地址，读取 `dns_ready`、`matched` 和 `checked_at`。

创建仅 IPv6 节点时，先查询服务器的 `public_host_ipv6` 与 `network/capabilities`。用普通部署字段加 `ingress_family: "ipv6"` 和初始 `network` 调用 `POST /api/v1/servers/{id}/nodes/preview`。初始策略使用 `network.ingress_family: "ipv6"` 和 `on_unavailable: "block"`。监听选 `public_ipv6` 或 `address`（指定时必填 `listen_address` 和 `listen_interface_id`）。访问地址选 `inherit_ipv6` 或 `override`（后者必填 `advertise_host`）。部署后可以通过节点网络接口附加出口或入口来源策略。

预览返回 `readiness.ready=true` 时，将原请求的 `port` 改为预览返回的端口，附上 `expected_ipv6`（预览的 `confirmation`）和固定的 32 位十六进制 `operation_id`，提交原部署接口。201/202 返回节点 ID。超时后用同一请求重试，或按节点 ID 核对 `GET /api/v1/nodes/{id}`。仅在 `ipv6_ingress_ready=true` 后请求节点 URI。条件变化返回 409 时重新预览。已有节点可在网络预览中指定 `network.ingress_family`。显式选择 `default` 才能迁回普通接入。

入口来源模式使用 `network.ingress`：`relay_only` 需要 `source_cidrs`。`deny_countries` 需要 `countries`，先从 `GET /api/v1/geoip/countries` 读取管理员所选地址库支持的国家代码。控制端据此生成匹配的地址段。当前支持 `dbip-country-lite` 和 `maxmind-geolite2-country`。成功后查询 `/api/v1/network/operations/{op}`，等待 `status=applied`；超时先查原操作，不能把 202 当成 agent 已应用。管理员可调用 `POST /api/v1/geoip/refresh` 更新所有地区拒绝节点的固定数据版本并重新发布，随后逐台核对 desired 和 agent 回执。

`GET /api/v1/me/templates/{id}/versions` 和 `GET /api/v1/templates/{id}/versions` 分别读取本人及公共模板的加密历史快照（返回时为解密内容），最多 30 条。恢复时以选中的历史内容创建副本，预览后再切换应用；使用中的个人模板不能直接修改。历史内容可能包含秘密，不应回显。

委派成员查看他人分享时，可按分享 ID 授予 `shares.view` 以读取状态和配额。若还需读取订阅链接和客户端凭据，须额外授予 `shares.links`。分享绑定用户可按归属访问自己的链接。服务器范围只有在分享的全部专属目标和附加节点都落在该范围内时才覆盖这份分享。对受委派的非归属成员，备注、网络目标和附加节点 ID 始终隐藏。读取详情时，订阅和专属节点连接数据仅在同时具备链接权限时返回。`shares.create` 可按服务器范围授予，但被授权账户创建的分享只能绑定自己；暂停、恢复、重置、撤销、重签和删除可按分享范围分别授予。配额、目标和网络编辑仍要求管理员，避免把资源范围误当成服务器或部署权限。

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

## 1.8 节点换端口与人工不可用记录

换端口复用节点网络预览及提交接口，新增可选 `listen_port`，保留已有网络策略；返回的稳定网络操作编号用于查询、版本冲突检查及失败重试。客户端订阅仅使用已被 agent 确认的配置，`status=applied` 不代表用户本地可达。延迟、代理请求与分段探测接口及工作流已移除。

管理员 read scope 可 GET `/api/v1/servers/{id}/unavailable-ports` 或 `/api/v1/servers/{id}/unavailable-ports/{port}`。管理员 write scope 可 PUT/DELETE 单条记录地址，无 Cookie 可执行。PUT 正文为 `{"transport":"tcp","network":"本地网络或地区","reason":"人工观察到的原因"}`，transport 必填且为 tcp/udp/both；network/reason 可选，去除首尾空格后分别最多 120/500 UTF-8 字节，不允许 NUL。端口为 1–65535，每台服务器最多 512 条。

服务器 ID+端口为稳定资源键。PUT 200 同步保存并审计；完整重放更新同一条记录，覆盖传输、网络和原因。响应为 server_id/port/transport/network/reason/created_at/updated_at。断连先 GET 同条核对；DELETE 204 可重放，删除后 GET 404。普通用户不能读取或写入服务器端口记录；有 ai.access 权限时可使用受账户权限约束的开发者 key。服务器不存在返回 404，上限返回 409。记录不改节点或流量，不自动执行任何探测；删除节点不删除记录，删除服务器时级联清理。

换端口 preview 的 `unavailable_port` 可选字段提供目标端口匹配的人工记录，不影响 ready 的配置准入判定，不是实时网络结论。自动化应向用户说明记录适用的网络和传输，再按既有预览、提交和回执流程执行。普通地区 IP／域名不是远程探测服务，不能从其位置发起 Ping/TCPing；本地视角测试仍需当地实际探测端。
