# 第三方声明

VpsCT 的许可证适用于本项目原创代码，不替代第三方组件各自的许可证。
下表按锁定的依赖版本生成，原始版权和许可文本位于 `third_party/`，随发行包和容器一起提供。
清单覆盖 Go 模块图及 npm 非开发依赖；部分类型包或命令行依赖不会进入最终浏览器产物。

更新依赖后运行 `go mod download all`、`cd web && npm ci`，然后 `python3 scripts/third-party.py`。

## 1. 运行时外部组件与数据

- [sing-box](https://github.com/SagerNet/sing-box)：agent 从上游下载指定版本，本仓库及发行包不包含该内核二进制。
- [Snell Server](https://manual.nssurge.com/others/snell.html)：agent 从上游下载指定版本，使用和分发应遵循其自身条款。
- [Caddy](https://caddyserver.com/docs/install)：可选 HTTPS 代理，由安装器从官方软件源安装。
- [ip2region 数据](https://github.com/lionsoul2014/ip2region)：控制端运行时下载 XDB 数据，数据来源与许可见上游。
- 在线 IP 查询服务及连接日志行为见 [隐私与数据说明](docs/privacy.md)。

## 2. 构建依赖

| 生态 | 组件 | 版本 | 许可与原始声明 |
|---|---|---|---|
| go-runtime | Go standard library and runtime | go1.26.8 | BSD-3-Clause; [LICENSE](third_party/go-runtime/go1.26.8/LICENSE), [PATENTS](third_party/go-runtime/go1.26.8/PATENTS) |
| go | github.com/dustin/go-humanize | v1.0.1 | See license text; [LICENSE](third_party/go/github.com_dustin_go-humanize_v1.0.1/LICENSE) |
| go | github.com/google/pprof | v0.0.0-20260802141513-ef3492d7dac3 | See license text; [LICENSE](third_party/go/github.com_google_pprof_v0.0.0-20260802141513-ef3492d7dac3/LICENSE), [LICENSE](third_party/go/github.com_google_pprof_v0.0.0-20260802141513-ef3492d7dac3/third_party/svgpan/LICENSE) |
| go | github.com/google/uuid | v1.6.0 | See license text; [LICENSE](third_party/go/github.com_google_uuid_v1.6.0/LICENSE) |
| go | github.com/hashicorp/golang-lru/v2 | v2.0.7 | See license text; [LICENSE](third_party/go/github.com_hashicorp_golang-lru_v2_v2.0.7/LICENSE), [LICENSE_list](third_party/go/github.com_hashicorp_golang-lru_v2_v2.0.7/simplelru/LICENSE_list) |
| go | github.com/lionsoul2014/ip2region/binding/golang | v0.0.0-20260901011515-c1a1fc7d5941 | See license text; [LICENSE.md](third_party/go/github.com_lionsoul2014_ip2region_binding_golang_v0.0.0-20260901011515-c1a1fc7d5941/LICENSE.md) |
| go | github.com/mattn/go-isatty | v0.0.24 | See license text; [LICENSE](third_party/go/github.com_mattn_go-isatty_v0.0.24/LICENSE) |
| go | github.com/mitchellh/go-homedir | v1.1.0 | See license text; [LICENSE](third_party/go/github.com_mitchellh_go-homedir_v1.1.0/LICENSE) |
| go | github.com/ncruces/go-strftime | v1.0.0 | See license text; [LICENSE](third_party/go/github.com_ncruces_go-strftime_v1.0.0/LICENSE) |
| go | github.com/remyoudompheng/bigfft | v0.0.0-20230129092748-24d4a6f8daec | See license text; [LICENSE](third_party/go/github.com_remyoudompheng_bigfft_v0.0.0-20230129092748-24d4a6f8daec/LICENSE) |
| go | golang.org/x/crypto | v0.57.0 | See license text; [LICENSE](third_party/go/golang.org_x_crypto_v0.57.0/LICENSE) |
| go | golang.org/x/mod | v0.41.0 | See license text; [LICENSE](third_party/go/golang.org_x_mod_v0.41.0/LICENSE) |
| go | golang.org/x/net | v0.58.0 | See license text; [LICENSE](third_party/go/golang.org_x_net_v0.58.0/LICENSE) |
| go | golang.org/x/sync | v0.23.0 | See license text; [LICENSE](third_party/go/golang.org_x_sync_v0.23.0/LICENSE) |
| go | golang.org/x/sys | v0.48.0 | See license text; [LICENSE](third_party/go/golang.org_x_sys_v0.48.0/LICENSE) |
| go | golang.org/x/term | v0.46.0 | See license text; [LICENSE](third_party/go/golang.org_x_term_v0.46.0/LICENSE) |
| go | golang.org/x/text | v0.42.0 | See license text; [LICENSE](third_party/go/golang.org_x_text_v0.42.0/LICENSE) |
| go | golang.org/x/tools | v0.49.0 | See license text; [LICENSE](third_party/go/golang.org_x_tools_v0.49.0/LICENSE) |
| go | gopkg.in/check.v1 | v0.0.0-20161208181325-20d25e280405 | See license text; [LICENSE](third_party/go/gopkg.in_check.v1_v0.0.0-20161208181325-20d25e280405/LICENSE) |
| go | gopkg.in/yaml.v3 | v3.0.1 | See license text; [LICENSE](third_party/go/gopkg.in_yaml.v3_v3.0.1/LICENSE), [NOTICE](third_party/go/gopkg.in_yaml.v3_v3.0.1/NOTICE) |
| go | modernc.org/cc/v4 | v4.29.2 | See license text; [LICENSE](third_party/go/modernc.org_cc_v4_v4.29.2/LICENSE), [LICENSE](third_party/go/modernc.org_cc_v4_v4.29.2/testdata/jhjourdan/LICENSE) |
| go | modernc.org/ccgo/v4 | v4.35.0 | See license text; [LICENSE](third_party/go/modernc.org_ccgo_v4_v4.35.0/LICENSE) |
| go | modernc.org/fileutil | v1.4.0 | See license text; [LICENSE](third_party/go/modernc.org_fileutil_v1.4.0/LICENSE), [LICENSE](third_party/go/modernc.org_fileutil_v1.4.0/falloc/LICENSE), [LICENSE](third_party/go/modernc.org_fileutil_v1.4.0/hdb/LICENSE), [LICENSE](third_party/go/modernc.org_fileutil_v1.4.0/storage/LICENSE) |
| go | modernc.org/gc/v2 | v2.6.5 | See license text; [LICENSE](third_party/go/modernc.org_gc_v2_v2.6.5/LICENSE) |
| go | modernc.org/gc/v3 | v3.1.5 | See license text; [LICENSE](third_party/go/modernc.org_gc_v3_v3.1.5/LICENSE) |
| go | modernc.org/goabi0 | v0.2.0 | See license text; [LICENSE](third_party/go/modernc.org_goabi0_v0.2.0/LICENSE) |
| go | modernc.org/libc | v1.75.7 | See license text; [LICENSE](third_party/go/modernc.org_libc_v1.75.7/LICENSE), [LICENSE-3RD-PARTY.md](third_party/go/modernc.org_libc_v1.75.7/LICENSE-3RD-PARTY.md), [COPYRIGHT](third_party/go/modernc.org_libc_v1.75.7/testdata/nsz.repo.hu/libc-test/COPYRIGHT), [COPYING](third_party/go/modernc.org_libc_v1.75.7/testdata/nsz.repo.hu/libc-test/src/math/crlibm/COPYING) |
| go | modernc.org/mathutil | v1.7.1 | See license text; [LICENSE](third_party/go/modernc.org_mathutil_v1.7.1/LICENSE), [LICENSE](third_party/go/modernc.org_mathutil_v1.7.1/mersenne/LICENSE) |
| go | modernc.org/memory | v1.12.1 | See license text; [LICENSE](third_party/go/modernc.org_memory_v1.12.1/LICENSE), [LICENSE-GO](third_party/go/modernc.org_memory_v1.12.1/LICENSE-GO), [LICENSE-LOGO](third_party/go/modernc.org_memory_v1.12.1/LICENSE-LOGO), [LICENSE-MMAP-GO](third_party/go/modernc.org_memory_v1.12.1/LICENSE-MMAP-GO) |
| go | modernc.org/opt | v0.2.0 | See license text; [LICENSE](third_party/go/modernc.org_opt_v0.2.0/LICENSE) |
| go | modernc.org/sortutil | v1.2.1 | See license text; [LICENSE](third_party/go/modernc.org_sortutil_v1.2.1/LICENSE) |
| go | modernc.org/sqlite | v1.58.0 | See license text; [LICENSE](third_party/go/modernc.org_sqlite_v1.58.0/LICENSE), [LICENSE-SQLITE](third_party/go/modernc.org_sqlite_v1.58.0/LICENSE-SQLITE), [LICENSE-SQLITE_VEC](third_party/go/modernc.org_sqlite_v1.58.0/LICENSE-SQLITE_VEC) |
| go | modernc.org/strutil | v1.2.1 | See license text; [LICENSE](third_party/go/modernc.org_strutil_v1.2.1/LICENSE) |
| go | modernc.org/token | v1.1.0 | See license text; [LICENSE](third_party/go/modernc.org_token_v1.1.0/LICENSE) |
| npm | @babel/runtime | 7.29.7 | MIT; [LICENSE](third_party/npm/_babel_runtime_7.29.7/LICENSE) |
| npm | @dnd-kit/accessibility | 3.1.1 | MIT; [LICENSE](third_party/npm/_dnd-kit_accessibility_3.1.1/LICENSE) |
| npm | @dnd-kit/core | 6.3.1 | MIT; [LICENSE](third_party/npm/_dnd-kit_core_6.3.1/LICENSE) |
| npm | @dnd-kit/sortable | 8.0.0 | MIT; [LICENSE](third_party/npm/_dnd-kit_sortable_8.0.0/LICENSE) |
| npm | @dnd-kit/utilities | 3.2.2 | MIT; [LICENSE](third_party/npm/_dnd-kit_utilities_3.2.2/LICENSE) |
| npm | @tanstack/query-core | 5.102.8 | MIT; [LICENSE](third_party/npm/_tanstack_query-core_5.102.8/LICENSE) |
| npm | @tanstack/react-query | 5.102.8 | MIT; [LICENSE](third_party/npm/_tanstack_react-query_5.102.8/LICENSE) |
| npm | @types/d3-array | 3.2.2 | MIT; [LICENSE](third_party/npm/_types_d3-array_3.2.2/LICENSE) |
| npm | @types/d3-color | 3.1.3 | MIT; [LICENSE](third_party/npm/_types_d3-color_3.1.3/LICENSE) |
| npm | @types/d3-ease | 3.0.2 | MIT; [LICENSE](third_party/npm/_types_d3-ease_3.0.2/LICENSE) |
| npm | @types/d3-interpolate | 3.0.4 | MIT; [LICENSE](third_party/npm/_types_d3-interpolate_3.0.4/LICENSE) |
| npm | @types/d3-path | 3.1.1 | MIT; [LICENSE](third_party/npm/_types_d3-path_3.1.1/LICENSE) |
| npm | @types/d3-scale | 4.0.9 | MIT; [LICENSE](third_party/npm/_types_d3-scale_4.0.9/LICENSE) |
| npm | @types/d3-shape | 3.2.0 | MIT; [LICENSE](third_party/npm/_types_d3-shape_3.2.0/LICENSE) |
| npm | @types/d3-time | 3.0.4 | MIT; [LICENSE](third_party/npm/_types_d3-time_3.0.4/LICENSE) |
| npm | @types/d3-timer | 3.0.2 | MIT; [LICENSE](third_party/npm/_types_d3-timer_3.0.2/LICENSE) |
| npm | ansi-regex | 5.0.1 | MIT; [license](third_party/npm/ansi-regex_5.0.1/license) |
| npm | ansi-styles | 4.3.0 | MIT; [license](third_party/npm/ansi-styles_4.3.0/license) |
| npm | camelcase | 5.3.1 | MIT; [license](third_party/npm/camelcase_5.3.1/license) |
| npm | cliui | 6.0.0 | ISC; [LICENSE.txt](third_party/npm/cliui_6.0.0/LICENSE.txt) |
| npm | clsx | 2.1.1 | MIT; [license](third_party/npm/clsx_2.1.1/license) |
| npm | color-convert | 2.0.1 | MIT; [LICENSE](third_party/npm/color-convert_2.0.1/LICENSE) |
| npm | color-name | 1.1.4 | MIT; [LICENSE](third_party/npm/color-name_1.1.4/LICENSE) |
| npm | cookie | 1.1.1 | MIT; [LICENSE](third_party/npm/cookie_1.1.1/LICENSE) |
| npm | csstype | 3.2.3 | MIT; [LICENSE](third_party/npm/csstype_3.2.3/LICENSE) |
| npm | d3-array | 3.2.4 | ISC; [LICENSE](third_party/npm/d3-array_3.2.4/LICENSE) |
| npm | d3-color | 3.1.0 | ISC; [LICENSE](third_party/npm/d3-color_3.1.0/LICENSE) |
| npm | d3-ease | 3.0.1 | BSD-3-Clause; [LICENSE](third_party/npm/d3-ease_3.0.1/LICENSE) |
| npm | d3-format | 3.1.2 | ISC; [LICENSE](third_party/npm/d3-format_3.1.2/LICENSE) |
| npm | d3-interpolate | 3.0.1 | ISC; [LICENSE](third_party/npm/d3-interpolate_3.0.1/LICENSE) |
| npm | d3-path | 3.1.0 | ISC; [LICENSE](third_party/npm/d3-path_3.1.0/LICENSE) |
| npm | d3-scale | 4.0.2 | ISC; [LICENSE](third_party/npm/d3-scale_4.0.2/LICENSE) |
| npm | d3-shape | 3.2.0 | ISC; [LICENSE](third_party/npm/d3-shape_3.2.0/LICENSE) |
| npm | d3-time | 3.1.0 | ISC; [LICENSE](third_party/npm/d3-time_3.1.0/LICENSE) |
| npm | d3-time-format | 4.1.0 | ISC; [LICENSE](third_party/npm/d3-time-format_4.1.0/LICENSE) |
| npm | d3-timer | 3.0.1 | ISC; [LICENSE](third_party/npm/d3-timer_3.0.1/LICENSE) |
| npm | decamelize | 1.2.0 | MIT; [license](third_party/npm/decamelize_1.2.0/license) |
| npm | decimal.js-light | 2.5.1 | MIT; [LICENCE.md](third_party/npm/decimal.js-light_2.5.1/LICENCE.md) |
| npm | dijkstrajs | 1.0.3 | MIT; [LICENSE.md](third_party/npm/dijkstrajs_1.0.3/LICENSE.md) |
| npm | dom-helpers | 5.2.1 | MIT; [LICENSE](third_party/npm/dom-helpers_5.2.1/LICENSE) |
| npm | emoji-regex | 8.0.0 | MIT; [LICENSE-MIT.txt](third_party/npm/emoji-regex_8.0.0/LICENSE-MIT.txt) |
| npm | eventemitter3 | 4.0.7 | MIT; [LICENSE](third_party/npm/eventemitter3_4.0.7/LICENSE) |
| npm | fast-equals | 5.4.2 | MIT; [LICENSE](third_party/npm/fast-equals_5.4.2/LICENSE) |
| npm | find-up | 4.1.0 | MIT; [license](third_party/npm/find-up_4.1.0/license) |
| npm | get-caller-file | 2.0.5 | ISC; [LICENSE.md](third_party/npm/get-caller-file_2.0.5/LICENSE.md) |
| npm | internmap | 2.0.3 | ISC; [LICENSE](third_party/npm/internmap_2.0.3/LICENSE) |
| npm | is-fullwidth-code-point | 3.0.0 | MIT; [license](third_party/npm/is-fullwidth-code-point_3.0.0/license) |
| npm | js-tokens | 4.0.0 | MIT; [LICENSE](third_party/npm/js-tokens_4.0.0/LICENSE) |
| npm | locate-path | 5.0.0 | MIT; [license](third_party/npm/locate-path_5.0.0/license) |
| npm | lodash | 4.18.1 | MIT; [LICENSE](third_party/npm/lodash_4.18.1/LICENSE) |
| npm | loose-envify | 1.4.0 | MIT; [LICENSE](third_party/npm/loose-envify_1.4.0/LICENSE) |
| npm | lucide-react | 0.447.0 | ISC; [LICENSE](third_party/npm/lucide-react_0.447.0/LICENSE), [copyright.js.map](third_party/npm/lucide-react_0.447.0/dist/esm/icons/copyright.js.map) |
| npm | object-assign | 4.1.1 | MIT; [license](third_party/npm/object-assign_4.1.1/license) |
| npm | p-limit | 2.3.0 | MIT; [license](third_party/npm/p-limit_2.3.0/license) |
| npm | p-locate | 4.1.0 | MIT; [license](third_party/npm/p-locate_4.1.0/license) |
| npm | p-try | 2.2.0 | MIT; [license](third_party/npm/p-try_2.2.0/license) |
| npm | path-exists | 4.0.0 | MIT; [license](third_party/npm/path-exists_4.0.0/license) |
| npm | pngjs | 5.0.0 | MIT; [LICENSE](third_party/npm/pngjs_5.0.0/LICENSE) |
| npm | prop-types | 15.8.1 | MIT; [LICENSE](third_party/npm/prop-types_15.8.1/LICENSE) |
| npm | react-is | 16.13.1 | MIT; [LICENSE](third_party/npm/react-is_16.13.1/LICENSE) |
| npm | qrcode | 1.5.4 | MIT; [license](third_party/npm/qrcode_1.5.4/license) |
| npm | react | 18.3.1 | MIT; [LICENSE](third_party/npm/react_18.3.1/LICENSE) |
| npm | react-dom | 18.3.1 | MIT; [LICENSE](third_party/npm/react-dom_18.3.1/LICENSE) |
| npm | react-is | 18.3.1 | MIT; [LICENSE](third_party/npm/react-is_18.3.1/LICENSE) |
| npm | react-router | 7.18.3 | MIT; [LICENSE.md](third_party/npm/react-router_7.18.3/LICENSE.md) |
| npm | react-router-dom | 7.18.3 | MIT; [LICENSE.md](third_party/npm/react-router-dom_7.18.3/LICENSE.md) |
| npm | react-smooth | 4.0.4 | MIT; [LICENSE](third_party/npm/react-smooth_4.0.4/LICENSE) |
| npm | react-transition-group | 4.4.5 | BSD-3-Clause; [LICENSE](third_party/npm/react-transition-group_4.4.5/LICENSE) |
| npm | recharts | 2.15.4 | MIT; [LICENSE](third_party/npm/recharts_2.15.4/LICENSE) |
| npm | recharts-scale | 0.4.5 | MIT; [LICENSE](third_party/npm/recharts-scale_0.4.5/LICENSE) |
| npm | require-directory | 2.1.1 | MIT; [LICENSE](third_party/npm/require-directory_2.1.1/LICENSE) |
| npm | require-main-filename | 2.0.0 | ISC; [LICENSE.txt](third_party/npm/require-main-filename_2.0.0/LICENSE.txt) |
| npm | scheduler | 0.23.2 | MIT; [LICENSE](third_party/npm/scheduler_0.23.2/LICENSE) |
| npm | set-blocking | 2.0.0 | ISC; [LICENSE.txt](third_party/npm/set-blocking_2.0.0/LICENSE.txt) |
| npm | set-cookie-parser | 2.7.2 | MIT; [LICENSE](third_party/npm/set-cookie-parser_2.7.2/LICENSE) |
| npm | string-width | 4.2.3 | MIT; [license](third_party/npm/string-width_4.2.3/license) |
| npm | strip-ansi | 6.0.1 | MIT; [license](third_party/npm/strip-ansi_6.0.1/license) |
| npm | tailwind-merge | 2.6.1 | MIT; [LICENSE.md](third_party/npm/tailwind-merge_2.6.1/LICENSE.md) |
| npm | tiny-invariant | 1.3.3 | MIT; [LICENSE](third_party/npm/tiny-invariant_1.3.3/LICENSE) |
| npm | tslib | 2.8.1 | 0BSD; [LICENSE.txt](third_party/npm/tslib_2.8.1/LICENSE.txt) |
| npm | victory-vendor | 36.9.2 | MIT AND ISC; [LICENSE](third_party/npm/victory-vendor_36.9.2/lib-vendor/d3-array/LICENSE), [LICENSE](third_party/npm/victory-vendor_36.9.2/lib-vendor/d3-color/LICENSE), [LICENSE](third_party/npm/victory-vendor_36.9.2/lib-vendor/d3-ease/LICENSE), [LICENSE](third_party/npm/victory-vendor_36.9.2/lib-vendor/d3-format/LICENSE), [LICENSE](third_party/npm/victory-vendor_36.9.2/lib-vendor/d3-interpolate/LICENSE), [LICENSE](third_party/npm/victory-vendor_36.9.2/lib-vendor/d3-path/LICENSE), [LICENSE](third_party/npm/victory-vendor_36.9.2/lib-vendor/d3-scale/LICENSE), [LICENSE](third_party/npm/victory-vendor_36.9.2/lib-vendor/d3-shape/LICENSE), [LICENSE](third_party/npm/victory-vendor_36.9.2/lib-vendor/d3-time/LICENSE), [LICENSE](third_party/npm/victory-vendor_36.9.2/lib-vendor/d3-time-format/LICENSE), [LICENSE](third_party/npm/victory-vendor_36.9.2/lib-vendor/d3-timer/LICENSE), [LICENSE](third_party/npm/victory-vendor_36.9.2/lib-vendor/d3-voronoi/LICENSE), [LICENSE](third_party/npm/victory-vendor_36.9.2/lib-vendor/internmap/LICENSE) |
| npm | which-module | 2.0.1 | ISC; [LICENSE](third_party/npm/which-module_2.0.1/LICENSE) |
| npm | wrap-ansi | 6.2.0 | MIT; [license](third_party/npm/wrap-ansi_6.2.0/license) |
| npm | y18n | 4.0.3 | ISC; [LICENSE](third_party/npm/y18n_4.0.3/LICENSE) |
| npm | yargs | 15.4.1 | MIT; [LICENSE](third_party/npm/yargs_15.4.1/LICENSE) |
| npm | yargs-parser | 18.1.3 | ISC; [LICENSE.txt](third_party/npm/yargs-parser_18.1.3/LICENSE.txt) |
