# 1. 常用业务请求

此文件提供已对照服务端请求结构的示例，不是完整 schema。先读取部署实例的 `/api/v1/agent/capabilities`，确认目标路由、角色、scope 与版本。示例中的 ID、名称与 `example.com` 均为占位；不代表用户的实际 VPS。

## 1.1 查询状态

| 目的 | 方法与路径 | 需要关注 |
|---|---|---|
| 仪表盘 | `GET /api/v1/dashboard` | 成员只返回可访问的分享和订阅 |
| 服务器列表/详情 | `GET /api/v1/servers`、`GET /api/v1/servers/{id}` | 管理员；包含地址，勿回显整份响应 |
| 本期流量 | `GET /api/v1/servers/{id}/traffic?period=current` | 需要设置月重置日；滚动查询改用 `?days=7` |
| 节点筛选 | `GET /api/v1/nodes?server_id=1&source=deployed` | 可另用 `q`、`external_sub_id`、`share_id`；返回 URI/凭据 |
| 期望配置 | `GET /api/v1/servers/{id}/desired` | 配置包含秘密；检查 revision/status 及 agent 已应用状态 |
| 网络操作结果 | `GET /api/v1/network/operations/{op}` | 使用真实响应中的操作编号 |
| 分享/订阅 | `GET /api/v1/shares`、`GET /api/v1/subscriptions` | 成员仅能读取自己有权访问的记录，响应含访问链接 |

## 1.2 添加服务器与部署节点

`POST /api/v1/servers` 的示例 body：

```json
{
  "name": "示例服务器",
  "region": "TEST",
  "public_host": "vps.example.com",
  "tags": [],
  "notes": "",
  "quota_bytes": 107374182400,
  "quota_reset_day": 1,
  "quota_billing": "dual",
  "core_mode": "stable",
  "ipv4_only": false,
  "cert_mode": "self_signed",
  "enabled": true
}
```

`quota_bytes=0` 表示不限量。计费方式 `dual`=入+出、`up`=入站、`down`=出站、`max`=整期两方向较大值。校准读数可使用 `quota_calibrated_inbound_bytes` 和 `quota_calibrated_outbound_bytes`，必须同时填写非负数且已配置重置日；请按用户实际服务商读数设置，不能推测。

已有记录通过 `PUT /api/v1/servers/{id}` 更新，先读再保留全份配置；遗漏普通字段可能将其重置。不要将详情中的派生信息、agent token 或 metrics 作为更新 body。

`POST /api/v1/servers/{id}/enroll-token` 无需 body，返回 15 分钟有效的 `token` 和 `install_command`。不要回显或持久化它们；生成安装命令不等于获准 SSH 到 VPS 执行。

`POST /api/v1/servers/{id}/nodes` 的基本部署 body：

```json
{"name":"示例 Snell 节点","protocol":"snell","port":0,"tags":[]}
```

`port=0` 由服务端分配。其它可用字段包括 `sni`、`domain`、`obfs`、`snell_version`、`mieru_transport`、`cert_id`、`cert_mode`，具体协议和能力以 `/api/v1/meta`、服务器网络能力及官方内核能力为准。服务端生成节点凭据，201 表示记录已创建；仍需查询服务器 desired/apply 状态。

## 1.3 订阅预览与创建

`POST /api/v1/subscriptions/preview`：

```json
{
  "name": "示例订阅",
  "kind": "generated",
  "default_format": "mihomo",
  "format": "mihomo",
  "node_selection": {"node_ids": [1], "external_sub_ids": []},
  "enabled": true
}
```

响应含 `format`、`body`、`node_names`、`groups`，`body` 可能含节点密码。确认预览后，移除仅用于预览的 `format`，向 `POST /api/v1/subscriptions` 提交。其它字段包括 `template_id`、`proxy_groups`、`chains`、`rules`、`rule_providers`、`expire_at`、`traffic_limit_bytes`、`reset_day`、`userinfo_header`、`show_info_nodes`、`allowed_user_ids`、`short_link`。空模板/代理组规则遵循模板默认语义，不能向其中写个人更新地址或网段。

创建订阅没有通用幂等 key；提交前可记录现有订阅 ID 与请求时间。创建请求超时后，通过列表/详情比对新增 ID、kind、节点选择、模板等完整请求字段及创建时间，不能只看同名记录；多个候选或证据不足时仍为结果未知，不再次 POST。用户索取链接时，可说明面板内的记录 ID/名称，让其在订阅详情领取，或使用其已授权的私有文件/秘密管理器交付，不在聊天展开 token。

转换外部订阅使用 `kind="imported"` 和 `source_external_id`；不接受 `uploaded_content`。`GET /api/v1/subscriptions/{id}/render?format=mihomo` 返回 JSON，其中 `body` 是客户端配置。轮换 token 使用 `POST /api/v1/subscriptions/{id}/rotate-token`，旧链接随之失效。

模板 `POST /api/v1/templates` 使用 `name`、`kind`、`description`、`content`、`variables`；`kind` 为 `mihomo`、`surge`、`singbox` 或 `shadowrocket`。预设 `POST /api/v1/presets` 使用 `name`、`groups`、`rules`，套用时需要把 groups 和 rules 一并写入订阅，不能只套一半。

## 1.4 创建和管理分享

`POST /api/v1/shares`：

```json
{
  "name": "示例分享",
  "user_id": null,
  "targets": [{"server_id": 1, "protocols": ["snell"]}],
  "extra_node_ids": [],
  "quota_bytes": 10737418240,
  "billing_mode": "dual",
  "reset_day": 1,
  "expires_at": null,
  "template_id": null,
  "connlog_enabled": false,
  "notes": ""
}
```

`user_id` 可指向真实已存在成员，`targets` 为要生成专用节点的目标。`extra_node_ids` 是复用节点，不产生独立分享节点计量。expires_at 使用 RFC 3339 时间，quota/billing/reset 与服务器相同公式。分享不接受订阅预设覆盖。

暂停/恢复分别为 `POST /api/v1/shares/{id}/pause`、`POST /api/v1/shares/{id}/resume`，无需 body。`reset` 会改变用量，`revoke`/`reissue` 会改变凭据，均为写操作且需属于用户请求。创建响应含节点/订阅秘密；即使返回 201，也应检查专用节点是否真正应用。

## 1.5 网络变更与维护

网络 preview 字段取决于目标资源，复用对应实例返回的确认凭证，不伪造它们。服务器网络能力为 `GET /api/v1/servers/{id}/network/capabilities`，当前网络为 `/network`。网络操作重试使用 `POST /api/v1/network/operations/{op}/retry`，body 为：

```json
{"expected_retry_revision": 0}
```

实际 revision 必须来自读取结果，示例 0 不能原样用于真实重试。409 后重新查询状态。

控制端维护前检查 `GET /api/v1/system/maintenance` 和 `GET /api/v1/system/maintenance/latest`；agent 维护前检查 `GET /api/v1/servers/{id}/maintenance`。维护请求统一字段为 `id`（32 位小写十六进制任务编号）、`role`（`controller`/`agent`）、`action`（`update`/`uninstall`）、`version`、`purge`、`remove_caddy`、`delete_server`、`password`、`code`、`confirm`。使用权限为 0600 的临时 body 文件，密码/验证码不进入命令参数或聊天。

目标路径为 `POST /api/v1/system/maintenance` 或 `POST /api/v1/servers/{id}/maintenance`。update 需正式发行版本且不可 purge；agent 版本必须与控制端当前提供的发行版相符。卸载控制端 `confirm="VpsCT"`，卸载 agent 使用完整服务器名称；卸载不传 version，默认不清空数据。`remove_caddy` 只用于控制端卸载且须 `purge=true`；`delete_server` 只用于 agent 卸载。具体执行能力以当前维护状态为准。

为一次维护生成一个 task id；请求断连后查询该 id 的状态，不生成新 id 盲目再次提交。202/queued/running 均不能报告成功。

## 1.6 设置与动态字段

`PUT /api/v1/settings` 是合并更新，只提交本次要修改的允许键；可写键及范围查看部署版本的接口合约。不要把 `GET /api/v1/settings` 整份回传，其中可能包含内部键、不可写键和密钥掩码。在线 GeoIP 默认关闭，启用需用户明确请求。

手工节点 `params`、出口 `config`、订阅 `rule_providers` 等字段使用不透明 JSON，通用 OpenAPI object 只表示外层类型。内部字段以对应协议/出口的产品定义及已有合法配置为准；新建时若没有足够字段依据，先取得该类型的实际 schema，不能借用其他协议的参数或随意填入。

## 1.7 连接日志导出

`GET /api/v1/connlog/export` 返回 CSV，需使用 `--output` 保存。`from`、`to` 可用 RFC 3339 时间或 `YYYY-MM-DD`；另可按 `server_id`、`node_id`、`share_id`、`host`、`src` 过滤。导出忽略传入 `limit`/`offset`，按内部固定批量读取且有固有行数上限；不能用 offset 将 CSV 分页拼接成完整历史。客户端上限 16 MiB，较大结果应缩小 from/to 时间范围并核对范围边界与服务端上限。

## 1.8 转发线路：两台、三台及导入落地

能力目录的 `workflows` 给出从查询、预览、创建到核对结果的操作顺序，每一步含实时 `operation_id` 和 `available`。`available` 仅表示当前 key/角色能否调用；资源归属、端点变化、网络与内核能力仍在实际执行时校验。不要把整个 workflow 当成自动授权的批量任务。

三台结构为“入口 → 中转 → 落地节点”；两台结构为“入口 → 落地节点”。落地服务始终需要，可运行在第 2 台机器上。面板默认三台，API 不提供隐含中转，`ix_server_id=0` 或省略表示两台。以下三台预览示例的 ID 必须换成查询到的实际 ID；一次创建自行生成 32 位小写十六进制 `operation_id` 并保持固定：

```json
{
  "operation_id": "0123456789abcdef0123456789abcdef",
  "name": "示例线路",
  "entry_server_id": 1,
  "ix_server_id": 2,
  "landing_node_id": 3,
  "entry_port": 0,
  "ix_port": 0,
  "network": "tcp",
  "source_mode": "all",
  "source_cidrs": []
}
```

两台时明确设置 `ix_server_id=0`，同时不填写中转地址/端口/出口字段；受管落地须在入口以外的服务器。入口与中转不需重复部署代理节点，用户使用投影后的入口订阅。产品配置不会自动购买、建立物理专线；主机之间的网络与路由需预先接通。

1. 用 `GET /api/v1/nodes` 按 `q`、`source`、`server_id` 等筛选。落地支持 `deployed`、`imported`、`manual`，不是只支持 SS。用 `/nodes/parse` 和 `/nodes/import` 导入时通过私有 body 文件传 `text`，不要把 URI 放命令参数。
2. 向 `POST /api/v1/access-routes/preview` 提交选择。`network` 为 `tcp` 或 `both`，应根据节点的实际线缆传输协议选择；VLESS 的 UDP 业务并不等于要 UDP 转发。按实际能力/错误提示处理，不擅自降级。
3. 检查 `ready`、`waiting_authorization` 和 `checks`；不就绪先处理网络、节点状态或本机授权。成功后**完整使用返回的 `candidate`**，仅增加 `expected_impact=token`，向 `POST /api/v1/access-routes` 提交，不能重新拼接表单默认值。
4. 202 后按返回 `id` 查询 `GET /api/v1/access-routes/{op}`，关注 `stage`、`message`、`revision`。`pending_landing`、`pending_ix`、`pending_entry` 是待应用，`blocked` 是阻塞；只有 `applied` 才可继续。用有界轮询和合理间隔，不无限等待。
5. `GET /api/v1/access-routes/{op}/node` 仅在实际可用时返回入口节点与 `uri`，否则 409。响应包含认证信息，保存到私有输出文件；必要时按 `subscription_id` 检查订阅渲染。
6. 超时或断连后先 GET 原 `id`；不存在时也先确认是否请求尚在执行。创建支持同一 `operation_id` 和相同完整 candidate 的核对后重放；同编号但配置不同会冲突。409/428 重新读取和预览，不生成新编号盲目重建。
7. 用户要求停止时，GET 当前线路，把最新 `revision` 作为 `expected_revision` 提交 `DELETE /api/v1/access-routes/{op}`。继续查询至 `retired`；`stopping_entry`、`stopping_ix` 尚未清理完成。阻塞重试使用 `POST /api/v1/access-routes/{op}/retry` 和最新 `expected_revision`。不要直接删除其底层转发。

接口字段保留 `ix_*` 兼容已有客户端，这些字段的产品含义是“中转”，不表示所有专线均为 IX。导入订阅更新导致落地端点/身份变化时，线路可能阻塞，应重新核对并按产品流程处理。

## 1.9 已有节点更换出口

用户仍连接原节点，服务端再通过目标出口访问网站，与端口接入转发不同。

- 受管 VPS：使用 `/transits/preview`、`/transits`，从实际网络能力选择可用协议；源节点可为服务端已支持的 VLESS 等协议，不根据旧 SS 示例限制。查询 `/servers/{id}/transits` 的两端状态及 stage。
- 已有代理/网卡：先查询 `/servers/{id}/network` 和出口列表；按 OpenAPI `config` 的 `x-kind-schemas` 创建 direct、SOCKS5、SS-2022、SSH 或 WireGuard 出口。随后 `/nodes/{id}/network/preview` 预览绑定、`PUT /nodes/{id}/network` 提交，并查询返回的网络任务。
- 仅转发固定端口：使用 `/servers/{id}/forwards/preview` 和 `/servers/{id}/forwards`。多段线路交给 access-routes 管理，不在不同功能中重复创建。

这些步骤需实际 preview 凭证和 revision。完成条件由最新节点回执与网络任务决定，不能仅凭 PUT 成功或旧的 applied 状态认定本次修改成功。

# 2. 部署与恢复

## 2.1 部署前识别

控制端是 `ctlvpsd`；被控主机是 `ctlvps-agent`；AI 客户端使用 API key。先识别用户授权目标、操作系统/架构、原有安装方式、当前版本、反向代理、数据及密钥位置。主机清单由用户的实时管理工具提供，本文不提供任何实际服务器清单。没有目标主机操作权限时可准备方案与命令，不连接或修改主机。

| 安装方式 | 使用条件 | 执行入口 | 完成判定 |
|---|---|---|---|
| 原生控制端 | Debian/Ubuntu、systemd、Linux amd64 | 可信 Release 的 install.sh；源码脚本须明确 repo/version | ctlvpsd 服务正常、本机深度就绪检查、HTTPS 页面及登录正常 |
| Docker 控制端 | Linux AMD64、可用的本机 Docker Engine、Compose v2；包括 Alpine/OpenRC 宿主机 | 可信源码的 deploy/docker-compose.yml | 容器 running/healthy、重启计数稳定、卷持久化、HTTPS 正常 |
| 被控 VPS | Linux AMD64 及系统隔离能力 | 服务器 API 生成的短期安装命令，在明确授权的目标主机执行 | agent_status=online、能力满足请求、后续节点实际已应用 |

第一台控制端尚未运行时从用户指定的可信仓库/发行包读取这份 Skill；不能假定已有 API。不要硬编码个人面板域名、机器清单、GitHub owner 或 latest 版本。

## 2.2 原生控制端

从已校验的发行包执行；源码 checkout 的脚本使用用户确认的真实仓库和目标版本：

```bash
sudo bash install.sh --repo "$vpsct_repository" --version "$vpsct_version" --domain "$vpsct_domain"
```

已有 HTTPS 代理则用 `--site-url "$vpsct_origin" --no-proxy`，保留现有代理配置。`install.sh --help` 获取当前版本参数，不自行构造未支持的开关。安装需要该目标的 root 权限；首次管理员初始化必须携带数据目录的 setup-token，不能重开无令牌 setup。初始化凭据和 API key 由用户通过授权的账户流程配置，不能为自动化削弱这些要求。

安装后分别检查本机服务和深度就绪接口，再检查实际 HTTPS origin；本机就绪不表示公网反代配置已正确。初始化令牌、环境配置及 secrets.key 不回显、不提交仓库、不发送到其他主机。

## 2.3 Docker 控制端

使用包含 Dockerfile 和 deploy/ 的完整可信源码；先配置 `deploy/.env` 的 `CTLVPS_SITE_URL` 为用户实际 HTTPS origin，然后在源码目录执行：

```bash
cd deploy
docker compose config --quiet
docker compose up -d --build
docker compose ps
docker compose exec -T ctlvpsd wget -T 8 -qO- 'http://127.0.0.1:8080/healthz?ready=1'
```

Compose 默认 UID 10001、只读根文件系统，`/data` 和 `/keys` 为两个可写命名卷；端口仅绑定 `127.0.0.1:8080`。通过宿主机 Caddy/Nginx 等提供 HTTPS，可信代理只填写实际相邻代理 CIDR。默认 `ctlvps:latest` 是本机构建标签，不表示已发布可拉取的公共镜像。不要猜测镜像仓库地址。

初始化 token 位于容器 `/data/setup-token`；如需在终端处理，重定向到仓库外 0700 私有目录中的 0600 文件，通过用户授权渠道交付，不直接打印。数据卷与密钥卷必须一起持久化和备份，不能只拷贝 SQLite 文件。控制端容器不挂载 Docker socket，不使用 privileged 或关闭安全策略。

## 2.4 升级、恢复和卸载

先确定原安装方式；禁止把升级请求擅自变成原生/Docker 迁移。完成配置写入不等于完成升级，核对运行版本、实际目标文件、深度就绪、持久化数据和必要的业务回执。

- 原生安装：可信安装器 `--update`，按需求使用已支持的 `--auto-rollback`。通过产品维护 API 时仍需要受支持的本机 worker、密码及已启用的两步验证。备份包含数据库、加密密钥和安装配置，不能只换回旧二进制去读取已迁移数据。
- Docker：获取已校验目标源码/本地镜像后，在 `deploy/` 执行 `sh compose-upgrade.sh upgrade`，或 `sh compose-upgrade.sh upgrade "$vpsct_image"`。后者要求本机已有目标镜像，且 `.env` 的 `CTLVPS_IMAGE` 与目标一致。脚本先校验配置及挂载，再停服快照、更新和就绪检查，失败配套回退。
- Docker 恢复：`sh compose-upgrade.sh rollback "$vpsct_backup_directory"`，使用实际脚本返回的备份目录。恢复后普通 compose 操作前，核对 `CTLVPS_IMAGE` 与备份 rollback-tag 一致。脚本仅支持本机 Docker 引擎、默认 UID/路径和 `/data`、`/keys` 两个命名卷；自定义绑定目录/布局按实际备份方案手动处理，不绕过检查。
- 升级失败：区分 failed/rolled_back/unknown，先查服务、日志和恢复点，再采取下一步。保留可用的完整备份；控制端回退不会自动回退远端 VPS agent。
- 卸载仅在用户明确要求的目标与范围内执行。原生使用独立 uninstall.sh，先 `--dry-run`；默认保留数据，`--purge` 才清理。Docker 普通升级不执行 `docker compose down -v`，也不删除命名卷或整个 /opt/ctlvps 共享父目录。

# 3. 新功能验收

新增能力应同时具备：实时目录与请求合约、权限/运行前提、明确完成条件、可查询的资源或任务状态、失败后的恢复方式，以及匹配的 Skill 示例。仅提供网页按钮不算已支持 AI。首次信任建立、操作系统权限和敏感维护仍按实际授权边界执行，不以“agent 友好”为由取消验证。
