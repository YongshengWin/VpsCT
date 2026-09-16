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
| go | cloud.google.com/go/compute/metadata | v0.7.0 | See license text; [LICENSE](third_party/go/cloud.google.com_go_compute_metadata_v0.7.0/LICENSE) |
| go | github.com/Microsoft/go-winio | v0.6.2 | See license text; [LICENSE](third_party/go/github.com_Microsoft_go-winio_v0.6.2/LICENSE) |
| go | github.com/cenkalti/backoff/v5 | v5.0.3 | See license text; [LICENSE](third_party/go/github.com_cenkalti_backoff_v5_v5.0.3/LICENSE) |
| go | github.com/codahale/rfc6979 | v0.0.0-20141003034818-6a90f24967eb | See license text; [LICENSE](third_party/go/github.com_codahale_rfc6979_v0.0.0-20141003034818-6a90f24967eb/LICENSE) |
| go | github.com/containerd/errdefs | v1.0.0 | See license text; [LICENSE](third_party/go/github.com_containerd_errdefs_v1.0.0/LICENSE) |
| go | github.com/containerd/errdefs/pkg | v0.3.0 | See license text; [LICENSE](third_party/go/github.com_containerd_errdefs_pkg_v0.3.0/LICENSE) |
| go | github.com/containerd/log | v0.1.0 | See license text; [LICENSE](third_party/go/github.com_containerd_log_v0.1.0/LICENSE) |
| go | github.com/containerd/stargz-snapshotter/estargz | v0.18.1 | See license text; [LICENSE](third_party/go/github.com_containerd_stargz-snapshotter_estargz_v0.18.1/LICENSE) |
| go | github.com/coreos/go-oidc/v3 | v3.17.0 | See license text; [LICENSE](third_party/go/github.com_coreos_go-oidc_v3_v3.17.0/LICENSE), [NOTICE](third_party/go/github.com_coreos_go-oidc_v3_v3.17.0/NOTICE) |
| go | github.com/cpuguy83/go-md2man/v2 | v2.0.7 | See license text; [LICENSE.md](third_party/go/github.com_cpuguy83_go-md2man_v2_v2.0.7/LICENSE.md) |
| go | github.com/davecgh/go-spew | v1.1.1 | See license text; [LICENSE](third_party/go/github.com_davecgh_go-spew_v1.1.1/LICENSE) |
| go | github.com/distribution/reference | v0.6.0 | See license text; [LICENSE](third_party/go/github.com_distribution_reference_v0.6.0/LICENSE) |
| go | github.com/docker/cli | v29.0.3+incompatible | See license text; [LICENSE](third_party/go/github.com_docker_cli_v29.0.3_incompatible/LICENSE), [NOTICE](third_party/go/github.com_docker_cli_v29.0.3_incompatible/NOTICE), [LICENSE](third_party/go/github.com_docker_cli_v29.0.3_incompatible/cli/connhelper/internal/syntax/LICENSE) |
| go | github.com/docker/distribution | v2.8.3+incompatible | See license text; [LICENSE](third_party/go/github.com_docker_distribution_v2.8.3_incompatible/LICENSE) |
| go | github.com/docker/docker | v28.5.2+incompatible | See license text; [LICENSE](third_party/go/github.com_docker_docker_v28.5.2_incompatible/LICENSE), [NOTICE](third_party/go/github.com_docker_docker_v28.5.2_incompatible/NOTICE), [LICENSE](third_party/go/github.com_docker_docker_v28.5.2_incompatible/contrib/busybox/LICENSE) |
| go | github.com/docker/docker-credential-helpers | v0.9.3 | See license text; [LICENSE](third_party/go/github.com_docker_docker-credential-helpers_v0.9.3/LICENSE) |
| go | github.com/docker/go-connections | v0.5.0 | See license text; [LICENSE](third_party/go/github.com_docker_go-connections_v0.5.0/LICENSE) |
| go | github.com/docker/go-units | v0.5.0 | See license text; [LICENSE](third_party/go/github.com_docker_go-units_v0.5.0/LICENSE) |
| go | github.com/dustin/go-humanize | v1.0.1 | See license text; [LICENSE](third_party/go/github.com_dustin_go-humanize_v1.0.1/LICENSE) |
| go | github.com/felixge/httpsnoop | v1.0.4 | See license text; [LICENSE.txt](third_party/go/github.com_felixge_httpsnoop_v1.0.4/LICENSE.txt) |
| go | github.com/go-jose/go-jose/v4 | v4.1.3 | See license text; [LICENSE](third_party/go/github.com_go-jose_go-jose_v4_v4.1.3/LICENSE), [LICENSE](third_party/go/github.com_go-jose_go-jose_v4_v4.1.3/json/LICENSE) |
| go | github.com/go-logr/logr | v1.4.3 | See license text; [LICENSE](third_party/go/github.com_go-logr_logr_v1.4.3/LICENSE) |
| go | github.com/go-logr/stdr | v1.2.2 | See license text; [LICENSE](third_party/go/github.com_go-logr_stdr_v1.2.2/LICENSE) |
| go | github.com/go-rod/rod | v0.116.2 | See license text; [LICENSE](third_party/go/github.com_go-rod_rod_v0.116.2/LICENSE) |
| go | github.com/golang/protobuf | v1.5.0 | See license text; [LICENSE](third_party/go/github.com_golang_protobuf_v1.5.0/LICENSE) |
| go | github.com/golang/snappy | v0.0.4 | See license text; [LICENSE](third_party/go/github.com_golang_snappy_v0.0.4/LICENSE) |
| go | github.com/google/go-cmp | v0.7.0 | See license text; [LICENSE](third_party/go/github.com_google_go-cmp_v0.7.0/LICENSE) |
| go | github.com/google/go-containerregistry | v0.20.7 | See license text; [LICENSE](third_party/go/github.com_google_go-containerregistry_v0.20.7/LICENSE) |
| go | github.com/google/pprof | v0.0.0-20260802141513-ef3492d7dac3 | See license text; [LICENSE](third_party/go/github.com_google_pprof_v0.0.0-20260802141513-ef3492d7dac3/LICENSE), [LICENSE](third_party/go/github.com_google_pprof_v0.0.0-20260802141513-ef3492d7dac3/third_party/svgpan/LICENSE) |
| go | github.com/google/uuid | v1.6.0 | See license text; [LICENSE](third_party/go/github.com_google_uuid_v1.6.0/LICENSE) |
| go | github.com/hashicorp/golang-lru/v2 | v2.0.7 | See license text; [LICENSE](third_party/go/github.com_hashicorp_golang-lru_v2_v2.0.7/LICENSE), [LICENSE_list](third_party/go/github.com_hashicorp_golang-lru_v2_v2.0.7/simplelru/LICENSE_list) |
| go | github.com/inconshreveable/mousetrap | v1.1.0 | See license text; [LICENSE](third_party/go/github.com_inconshreveable_mousetrap_v1.1.0/LICENSE) |
| go | github.com/klauspost/compress | v1.18.1 | See license text; [LICENSE](third_party/go/github.com_klauspost_compress_v1.18.1/LICENSE), [LICENSE](third_party/go/github.com_klauspost_compress_v1.18.1/gzhttp/LICENSE), [LICENSE](third_party/go/github.com_klauspost_compress_v1.18.1/internal/lz4ref/LICENSE), [LICENSE](third_party/go/github.com_klauspost_compress_v1.18.1/internal/snapref/LICENSE), [LICENSE](third_party/go/github.com_klauspost_compress_v1.18.1/s2/LICENSE), [LICENSE](third_party/go/github.com_klauspost_compress_v1.18.1/s2/cmd/internal/filepathx/LICENSE), [LICENSE](third_party/go/github.com_klauspost_compress_v1.18.1/s2/cmd/internal/readahead/LICENSE), [LICENSE](third_party/go/github.com_klauspost_compress_v1.18.1/snappy/LICENSE), [LICENSE](third_party/go/github.com_klauspost_compress_v1.18.1/snappy/xerial/LICENSE), [LICENSE.txt](third_party/go/github.com_klauspost_compress_v1.18.1/zstd/internal/xxhash/LICENSE.txt) |
| go | github.com/letsencrypt/boulder | v0.20260223.0 | See license text; [LICENSE.txt](third_party/go/github.com_letsencrypt_boulder_v0.20260223.0/LICENSE.txt) |
| go | github.com/lionsoul2014/ip2region/binding/golang | v0.0.0-20260901011515-c1a1fc7d5941 | See license text; [LICENSE.md](third_party/go/github.com_lionsoul2014_ip2region_binding_golang_v0.0.0-20260901011515-c1a1fc7d5941/LICENSE.md) |
| go | github.com/mattn/go-isatty | v0.0.24 | See license text; [LICENSE](third_party/go/github.com_mattn_go-isatty_v0.0.24/LICENSE) |
| go | github.com/mitchellh/go-homedir | v1.1.0 | See license text; [LICENSE](third_party/go/github.com_mitchellh_go-homedir_v1.1.0/LICENSE) |
| go | github.com/moby/docker-image-spec | v1.3.1 | See license text; [LICENSE](third_party/go/github.com_moby_docker-image-spec_v1.3.1/LICENSE) |
| go | github.com/moby/sys/atomicwriter | v0.1.0 | See license text; [LICENSE](third_party/go/github.com_moby_sys_atomicwriter_v0.1.0/LICENSE) |
| go | github.com/moby/term | v0.0.0-20221205130635-1aeaba878587 | See license text; [LICENSE](third_party/go/github.com_moby_term_v0.0.0-20221205130635-1aeaba878587/LICENSE) |
| go | github.com/morikuni/aec | v1.0.0 | See license text; [LICENSE](third_party/go/github.com_morikuni_aec_v1.0.0/LICENSE) |
| go | github.com/ncruces/go-strftime | v1.0.0 | See license text; [LICENSE](third_party/go/github.com_ncruces_go-strftime_v1.0.0/LICENSE) |
| go | github.com/opencontainers/go-digest | v1.0.0 | See license text; [LICENSE](third_party/go/github.com_opencontainers_go-digest_v1.0.0/LICENSE), [LICENSE.docs](third_party/go/github.com_opencontainers_go-digest_v1.0.0/LICENSE.docs) |
| go | github.com/opencontainers/image-spec | v1.1.1 | See license text; [LICENSE](third_party/go/github.com_opencontainers_image-spec_v1.1.1/LICENSE) |
| go | github.com/pkg/browser | v0.0.0-20210911075715-681adbf594b8 | See license text; [LICENSE](third_party/go/github.com_pkg_browser_v0.0.0-20210911075715-681adbf594b8/LICENSE) |
| go | github.com/pkg/errors | v0.9.1 | See license text; [LICENSE](third_party/go/github.com_pkg_errors_v0.9.1/LICENSE) |
| go | github.com/pmezard/go-difflib | v1.0.0 | See license text; [LICENSE](third_party/go/github.com_pmezard_go-difflib_v1.0.0/LICENSE) |
| go | github.com/remyoudompheng/bigfft | v0.0.0-20230129092748-24d4a6f8daec | See license text; [LICENSE](third_party/go/github.com_remyoudompheng_bigfft_v0.0.0-20230129092748-24d4a6f8daec/LICENSE) |
| go | github.com/russross/blackfriday/v2 | v2.1.0 | See license text; [LICENSE.txt](third_party/go/github.com_russross_blackfriday_v2_v2.1.0/LICENSE.txt) |
| go | github.com/secure-systems-lab/go-securesystemslib | v0.11.0 | See license text; [LICENSE](third_party/go/github.com_secure-systems-lab_go-securesystemslib_v0.11.0/LICENSE) |
| go | github.com/sigstore/protobuf-specs | v0.5.0 | See license text; [COPYRIGHT.txt](third_party/go/github.com_sigstore_protobuf-specs_v0.5.0/COPYRIGHT.txt), [LICENSE](third_party/go/github.com_sigstore_protobuf-specs_v0.5.0/LICENSE), [LICENSE](third_party/go/github.com_sigstore_protobuf-specs_v0.5.0/gen/pb-python/LICENSE), [LICENSE](third_party/go/github.com_sigstore_protobuf-specs_v0.5.0/gen/pb-ruby/LICENSE), [LICENSE](third_party/go/github.com_sigstore_protobuf-specs_v0.5.0/gen/pb-typescript/LICENSE) |
| go | github.com/sigstore/sigstore | v1.10.6 | See license text; [COPYRIGHT.txt](third_party/go/github.com_sigstore_sigstore_v1.10.6/COPYRIGHT.txt), [LICENSE](third_party/go/github.com_sigstore_sigstore_v1.10.6/LICENSE) |
| go | github.com/sirupsen/logrus | v1.9.3 | See license text; [LICENSE](third_party/go/github.com_sirupsen_logrus_v1.9.3/LICENSE) |
| go | github.com/spf13/cobra | v1.10.2 | See license text; [LICENSE.txt](third_party/go/github.com_spf13_cobra_v1.10.2/LICENSE.txt) |
| go | github.com/spf13/pflag | v1.0.9 | See license text; [LICENSE](third_party/go/github.com_spf13_pflag_v1.0.9/LICENSE) |
| go | github.com/stretchr/testify | v1.11.1 | See license text; [LICENSE](third_party/go/github.com_stretchr_testify_v1.11.1/LICENSE) |
| go | github.com/syndtr/goleveldb | v1.0.1-0.20220721030215-126854af5e6d | See license text; [LICENSE](third_party/go/github.com_syndtr_goleveldb_v1.0.1-0.20220721030215-126854af5e6d/LICENSE) |
| go | github.com/theupdateframework/go-tuf | v0.7.0 | See license text; [LICENSE](third_party/go/github.com_theupdateframework_go-tuf_v0.7.0/LICENSE), [LICENSE.txt](third_party/go/github.com_theupdateframework_go-tuf_v0.7.0/client/python_interop/testdata/LICENSE.txt) |
| go | github.com/theupdateframework/go-tuf/v2 | v2.4.2 | See license text; [LICENSE](third_party/go/github.com_theupdateframework_go-tuf_v2_v2.4.2/LICENSE), [NOTICE](third_party/go/github.com_theupdateframework_go-tuf_v2_v2.4.2/NOTICE) |
| go | github.com/tink-crypto/tink-go/v2 | v2.6.0 | See license text; [LICENSE](third_party/go/github.com_tink-crypto_tink-go_v2_v2.6.0/LICENSE) |
| go | github.com/titanous/rocacheck | v0.0.0-20171023193734-afe73141d399 | See license text; [LICENSE](third_party/go/github.com_titanous_rocacheck_v0.0.0-20171023193734-afe73141d399/LICENSE) |
| go | github.com/vbatts/tar-split | v0.12.2 | See license text; [LICENSE](third_party/go/github.com_vbatts_tar-split_v0.12.2/LICENSE) |
| go | github.com/ysmood/fetchup | v0.2.3 | See license text; [LICENSE](third_party/go/github.com_ysmood_fetchup_v0.2.3/LICENSE) |
| go | github.com/ysmood/goob | v0.4.0 | See license text; [LICENSE](third_party/go/github.com_ysmood_goob_v0.4.0/LICENSE) |
| go | github.com/ysmood/got | v0.40.0 | See license text; [LICENSE](third_party/go/github.com_ysmood_got_v0.40.0/LICENSE), [LICENSE](third_party/go/github.com_ysmood_got_v0.40.0/lib/got-vscode-extension/LICENSE) |
| go | github.com/ysmood/gson | v0.7.3 | See license text; [LICENSE](third_party/go/github.com_ysmood_gson_v0.7.3/LICENSE) |
| go | github.com/ysmood/leakless | v0.9.0 | See license text; [LICENSE](third_party/go/github.com_ysmood_leakless_v0.9.0/LICENSE) |
| go | go.opentelemetry.io/auto/sdk | v1.1.0 | See license text; [LICENSE](third_party/go/go.opentelemetry.io_auto_sdk_v1.1.0/LICENSE) |
| go | go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp | v0.61.0 | See license text; [LICENSE](third_party/go/go.opentelemetry.io_contrib_instrumentation_net_http_otelhttp_v0.61.0/LICENSE) |
| go | go.opentelemetry.io/otel | v1.36.0 | See license text; [LICENSE](third_party/go/go.opentelemetry.io_otel_v1.36.0/LICENSE) |
| go | go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp | v1.33.0 | See license text; [LICENSE](third_party/go/go.opentelemetry.io_otel_exporters_otlp_otlptrace_otlptracehttp_v1.33.0/LICENSE) |
| go | go.opentelemetry.io/otel/metric | v1.36.0 | See license text; [LICENSE](third_party/go/go.opentelemetry.io_otel_metric_v1.36.0/LICENSE) |
| go | go.opentelemetry.io/otel/trace | v1.36.0 | See license text; [LICENSE](third_party/go/go.opentelemetry.io_otel_trace_v1.36.0/LICENSE) |
| go | golang.org/x/crypto | v0.57.0 | See license text; [LICENSE](third_party/go/golang.org_x_crypto_v0.57.0/LICENSE) |
| go | golang.org/x/mod | v0.41.0 | See license text; [LICENSE](third_party/go/golang.org_x_mod_v0.41.0/LICENSE) |
| go | golang.org/x/net | v0.58.0 | See license text; [LICENSE](third_party/go/golang.org_x_net_v0.58.0/LICENSE) |
| go | golang.org/x/oauth2 | v0.35.0 | See license text; [LICENSE](third_party/go/golang.org_x_oauth2_v0.35.0/LICENSE) |
| go | golang.org/x/sync | v0.23.0 | See license text; [LICENSE](third_party/go/golang.org_x_sync_v0.23.0/LICENSE) |
| go | golang.org/x/sys | v0.48.0 | See license text; [LICENSE](third_party/go/golang.org_x_sys_v0.48.0/LICENSE) |
| go | golang.org/x/term | v0.46.0 | See license text; [LICENSE](third_party/go/golang.org_x_term_v0.46.0/LICENSE) |
| go | golang.org/x/text | v0.42.0 | See license text; [LICENSE](third_party/go/golang.org_x_text_v0.42.0/LICENSE) |
| go | golang.org/x/time | v0.0.0-20210723032227-1f47c861a9ac | See license text; [LICENSE](third_party/go/golang.org_x_time_v0.0.0-20210723032227-1f47c861a9ac/LICENSE) |
| go | golang.org/x/tools | v0.49.0 | See license text; [LICENSE](third_party/go/golang.org_x_tools_v0.49.0/LICENSE) |
| go | google.golang.org/genproto | v0.0.0-20230706204954-ccb25ca9f130 | See license text; [LICENSE](third_party/go/google.golang.org_genproto_v0.0.0-20230706204954-ccb25ca9f130/LICENSE) |
| go | google.golang.org/genproto/googleapis/api | v0.0.0-20250825161204-c5933d9347a5 | See license text; [LICENSE](third_party/go/google.golang.org_genproto_googleapis_api_v0.0.0-20250825161204-c5933d9347a5/LICENSE) |
| go | google.golang.org/genproto/googleapis/rpc | v0.0.0-20250825161204-c5933d9347a5 | See license text; [LICENSE](third_party/go/google.golang.org_genproto_googleapis_rpc_v0.0.0-20250825161204-c5933d9347a5/LICENSE) |
| go | google.golang.org/grpc | v1.75.0 | See license text; [LICENSE](third_party/go/google.golang.org_grpc_v1.75.0/LICENSE), [NOTICE.txt](third_party/go/google.golang.org_grpc_v1.75.0/NOTICE.txt) |
| go | google.golang.org/protobuf | v1.36.11 | See license text; [LICENSE](third_party/go/google.golang.org_protobuf_v1.36.11/LICENSE) |
| go | gopkg.in/check.v1 | v0.0.0-20161208181325-20d25e280405 | See license text; [LICENSE](third_party/go/gopkg.in_check.v1_v0.0.0-20161208181325-20d25e280405/LICENSE) |
| go | gopkg.in/yaml.v3 | v3.0.1 | See license text; [LICENSE](third_party/go/gopkg.in_yaml.v3_v3.0.1/LICENSE), [NOTICE](third_party/go/gopkg.in_yaml.v3_v3.0.1/NOTICE) |
| go | gotest.tools/v3 | v3.0.3 | See license text; [LICENSE](third_party/go/gotest.tools_v3_v3.0.3/LICENSE), [LICENSE](third_party/go/gotest.tools_v3_v3.0.3/internal/difflib/LICENSE) |
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
