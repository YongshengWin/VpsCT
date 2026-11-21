# 依赖维护记录

[返回 README](../README.md) · [贡献指南](../CONTRIBUTING.md#4-依赖更新)

## 1. 日常更新规则

1. 停止 Dependabot 自动版本和安全更新 PR：默认分支不保留 `.github/dependabot.yml`，仓库设置关闭 Dependabot security updates，保留 Dependency graph、Dependabot alerts 与密钥扫描。`open-pull-requests-limit: 0` 只能停止版本更新，不能停止安全更新；参见 [GitHub 安全更新说明](https://docs.github.com/en/code-security/how-tos/secure-your-supply-chain/secure-your-dependencies/configure-security-updates)。设置生效后关闭现有机器人 PR，关闭 PR 不等于漏洞已修复。
2. 按漏洞、兼容性和实际需求人工集中维护，不自动追最新。React、React DOM 及类型包一起验证；Tailwind、tailwind-merge、PostCSS 和 Autoprefixer 一起验证。前端包含 `0.x` 依赖，小版本也可能超出现有兼容范围。常规升级必须有明确收益，安全升级以实际告警和审计结果为准。
3. 每个更新先通过安装、类型检查、构建及完整 CI。许可声明需要随依赖变更重新生成并提交，不能仅修改版本与锁文件。小版本也可能影响行为，通过检查后再合并，不启用自动合并。
4. 跨大版本升级需要独立的迁移 PR，说明兼容要求，完成代码调整和浏览器验证。已经公开的版本标签与附件保持不变，后续变更进入下一版本。

每周一新加坡时间 11:23（UTC 03:23）与手动安全审计只报告扫描结果，不修改依赖、不创建 PR 或 issue。PR 的安全门禁与发布前的新鲜审计继续保留；扫描失败不能用关闭告警、自动忽略或跳过检查解决。

### 1.1 2026-10-31 维护调整

1. 调查时开放的机器人 PR 为 #45、#47–#51，其中 #45 是 Vite 安全更新，其余为常规依赖更新。停止来源后关闭存量，必要修复纳入人工维护变更。
2. 当前 Vite 8.3.0 存在 6 条依赖告警，本轮更新至 8.3.5 并重新执行完整依赖审计；开发依赖也参与审计。
3. CI 实际发现 `GO-2026-6809`，修复版本为 Go 1.26.10。统一更新 Go 构建基线和 Docker 构建镜像，并用新工具链重新编译控制端与 agent；只更新 Go 模块版本不能修复标准库。参见 [官方漏洞记录](https://pkg.go.dev/vuln/GO-2026-6809)。
4. 运行依赖更新须同步许可产物；#49、#51 的失败日志包含许可新鲜度检查失败，不通过删除检查解决。本轮不全量吸收这些 PR 的常规升级。

## 2. 首批自动更新核验

核验日期：2026-09-15。基线为 `v0.1.0`。下表仅描述这些 PR 的原始提交，不代表对后续版本的永久结论。

| PR | 提议更新 | 核验结果与处理 |
|---|---|---|
| [#1](https://github.com/YongshengWin/VpsCT/pull/1) | TypeScript 5 → 7、Tailwind 3 → 4 | TypeScript 删除了当前使用的 `baseUrl` 选项，类型检查失败；单独运行 Vite 也因 Tailwind 4 的 PostCSS 接入方式变化而失败。该混合升级不合并，拆为编译工具和样式迁移。 |
| [#2](https://github.com/YongshengWin/VpsCT/pull/2)、[#5](https://github.com/YongshengWin/VpsCT/pull/5) | 分别升级 React DOM 或 React 18 → 19 | 两个 PR 分别造成 React 类型包 18/19 冲突，`npm ci` 报 `ERESOLVE`。原 PR 不合并，后续应把四个关联包放入同一个 React 迁移 PR。 |
| [#3](https://github.com/YongshengWin/VpsCT/pull/3) | tailwind-merge 2 → 3 | 安装、类型检查与构建通过，原 CI 在许可声明校验处失败。此外，上游明确 v3 对应 Tailwind 4，当前项目使用 Tailwind 3，不能只补许可就合并。保留现有 2.x，随样式迁移统一处理。 |
| [#4](https://github.com/YongshengWin/VpsCT/pull/4) | Recharts 2 → 3 | 流量图和速率图的 Tooltip formatter 类型不再匹配。原 PR 不合并，需在图表迁移中调整格式化逻辑并验证悬浮提示、堆叠与坐标轴。 |

核验方式与基线结果：

1. 分别导出 5 个 PR 的原始提交，使用各自的锁文件运行 `npm ci`、`npm run typecheck` 和 `npm exec vite build`。单独调用 Vite 用于排查打包问题，不能替代包含类型检查的 `npm run build`。
2. 现有版本的 10 项前端测试、类型检查和完整生产构建均通过；`npm audit --audit-level=high` 报告 0 个漏洞，GitHub Dependabot 无开放中的安全告警。
3. `npm outdated --json` 中所有条目的 `current` 与 `wanted` 一致，前端依赖已达到声明兼容范围内的最新版本，没有待补入的小版本或补丁。上述 5 个 PR 不合并，前端依赖保持不变，后续按第 3 节分别迁移。

## 3. 迁移时的检查重点

1. React：同时升级 `react`、`react-dom`、`@types/react`、`@types/react-dom`，检查路由、弹窗、拖拽、表单和图表。
2. Tailwind：按 [官方升级指南](https://tailwindcss.com/docs/upgrade-guide) 调整 PostCSS 或 Vite 接入、配置和工具类，再升级匹配的 tailwind-merge。[tailwind-merge 上游说明](https://github.com/dcastil/tailwind-merge) 明确了两代 Tailwind 的适用版本。
3. TypeScript：调整被移除的编译选项和路径别名，确认测试、类型检查、构建与开发服务器一致。
4. Recharts：按新的 Tooltip 类型处理空值、数字与标签，确认展示单位和流量方向不变，并在浏览器检查。
5. 任意运行依赖变更：重新生成许可声明，核对变更的依赖版本和原始许可证，再完成完整 CI。

## 4. 首轮分组检查

1. [#7](https://github.com/YongshengWin/VpsCT/pull/7) 提出 Go 间接依赖 `modernc.org/libc` 从 `v1.75.6` 升到 `v1.75.7`。补齐模块校验和与第三方声明，保留许可证原文，并要求数据库测试、完整 CI 和安装验证通过后合并。该更新进入主分支，不替换 `v0.1.0` 已发布附件。
2. [#8](https://github.com/YongshengWin/VpsCT/pull/8) 提出 `lucide-react` 从 `0.447.0` 升到 `0.577.0`。虽然被归类为小版本，它超出了当前 `^0.447.0` 范围；本次暂缓，后续单独检查图标和页面。前端常规更新因此限制为补丁，安全更新仍不受此限制。
