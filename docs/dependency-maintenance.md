# 依赖维护记录

[返回 README](../README.md) · [贡献指南](../CONTRIBUTING.md#4-依赖更新)

## 1. 日常更新规则

1. 常规更新按周执行，只自动提交当前大版本内的小版本和补丁。使用 Dependabot 的 `allow.update-types`，不限制安全更新；参见 [GitHub 配置说明](https://docs.github.com/en/code-security/reference/supply-chain-security/dependabot-options-reference#update-types-allow)。
2. React、React DOM 及类型包作为一组；Tailwind、tailwind-merge、PostCSS 和 Autoprefixer 作为一组；其他前端运行依赖、开发工具和 Go 依赖分别分组。安全更新单独成组。
3. 每个更新先通过安装、类型检查、构建及完整 CI。许可声明需要随依赖变更重新生成并提交，不能仅修改版本与锁文件。小版本也可能影响行为，通过检查后再合并，不启用自动合并。
4. 跨大版本升级需要独立的迁移 PR，说明兼容要求，完成代码调整和浏览器验证。已经公开的版本标签与附件保持不变，后续变更进入下一版本。

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
3. `npm outdated --json` 中所有条目的 `current` 与 `wanted` 一致，前端依赖已达到声明兼容范围内的最新版本，没有待补入的小版本或补丁。本次整理不升级运行依赖，后续按第 3 节分别迁移。

## 3. 迁移时的检查重点

1. React：同时升级 `react`、`react-dom`、`@types/react`、`@types/react-dom`，检查路由、弹窗、拖拽、表单和图表。
2. Tailwind：按 [官方升级指南](https://tailwindcss.com/docs/upgrade-guide) 调整 PostCSS 或 Vite 接入、配置和工具类，再升级匹配的 tailwind-merge。[tailwind-merge 上游说明](https://github.com/dcastil/tailwind-merge) 明确了两代 Tailwind 的适用版本。
3. TypeScript：调整被移除的编译选项和路径别名，确认测试、类型检查、构建与开发服务器一致。
4. Recharts：按新的 Tooltip 类型处理空值、数字与标签，确认展示单位和流量方向不变，并在浏览器检查。
5. 任意运行依赖变更：重新生成许可声明，核对变更的依赖版本和原始许可证，再完成完整 CI。
