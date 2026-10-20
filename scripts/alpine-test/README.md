# 1. Alpine/OpenRC 容器验收

## 1.1 运行

准备与 Docker 架构相同、已对照官方 Release SHA256 校验的 sing-box 1.14.1 musl 二进制，再执行：

```bash
SINGBOX_MUSL_BIN=/absolute/path/to/sing-box bash scripts/test-alpine-container.sh
```

默认分别运行 256 MiB、128 MiB 容器；单独调试可设置 `ALPINE_TEST_MEMORY=256m`，或设置 `512m`。128 MiB 是实验压力场景，不代表最低支持内存。仅在镜像构建时下载 Alpine 软件包，运行阶段使用 `--network none`。不连接真实 VPS，不使用宿主 systemd，不挂载宿主 cgroup 路径。

ARM64 的 256 MiB 和 128 MiB 矩阵已实际通过。128 MiB 的容器内存达到上限，出现大量 `memory.events` 回收/上限事件，`oom_kill` 为 0；这仍属于明显内存压力，不作生产容量承诺。日常部署推荐 512 MiB，256 MiB 仅建议少量节点和低并发。夹具和文件缓存也计入容器统计，不能用该峰值推算完整生产最低内存。

## 1.2 验收边界

镜像运行真实 Alpine 3.23 OpenRC init。夹具安装编译的静态 agent、固定官方 musl core，通过 production `core.SingBox.ApplyResources` 创建非 root 服务，检查实际 UID、capabilities、seccomp、mount/IPC/UTS namespace、只读根和管理配置隔离。客户端和 echo 服务放在另一个网络 namespace，SS 的 TCP 和 UDP 都经供应商式端口映射，再由实际 nftables 计量。

验收包括生产代理 cgroup 的零标记/伪造标记拒绝、真实 SS TCP/UDP 到本机与私网拒绝、计量随服务重启继续增长、节点 blocked 后拒绝连接、独立 OpenRC guard 恢复删除的出口防火墙、停服清空整个代理 cgroup（含额外进程）。同时检查 guard 的权限、seccomp 和只读 namespace、固定服务 RPC 的文件权限和调用者 UID 边界，以及损坏服务脚本时不执行脚本仍可清空固定 cgroup。

流量测试前原子替换实际 launcher 文件，确认两个 helper 的 PID 与可执行文件更新、原节点恢复、已停止且禁用的私网节点保持停止、待恢复记录删除；另验证 helper 已更新但记录留存时仍可恢复。代理流量验收完成后启动并停止真实 agent 服务，检查 128 MiB 硬预算、64 MiB Go 软预算、防降级状态持久化和启动就绪记录；伪造旧 PID、旧进程启动时间以及停服后的旧记录均不得通过就绪检查。单独重跑此 agent 启停检查可设置 `ALPINE_TEST_READY_ONLY=1`。

内核 seccomp 测试验证已有线程同步，以及代理和 guard 各自允许的 socket family。随后在各自独立的网络 namespace 复用已有 kernel node-accounting、egress-boundary 和 NAT 测试，验证精确包字节、来源流量、已有 conntrack 的阻断以及规则清理。

NAT 内核夹具也覆盖固定转发 TCP/UDP 的独立收发计量、规则重放、封禁既有连接及退役后冻结计数。另以实际 agent 配置收敛入口和 nft 命令验证面板映射启用、更改、清除、本机 root 文件优先、空映射停用和错误文件拒绝回退；没有完整节点配置回执时不得报告已应用。此项只在容器里访问固定本机 NAT 配置路径。

当前核心夹具直接安装测试二进制并调用 production core driver。首次安装、下载/enroll/HTTPS 与真实 agent 心跳另由第 1.4 节的独立安装夹具验证；控制端完整应用回执仍由网络与 agent/API 测试覆盖。两种 Linux 架构需分别运行，不能用单架构结果代替另一架构。

## 1.3 mieru 与网页维护扩展

官方 mita/mieru 3.37.0 二进制需独立核对发行包 SHA256 后提供。下面的测试只运行无外网临时容器；升级测试使用本地构建 agent，并明确替换发行验证入口，验证目标是服务生命周期及回滚，发行信任链由 secureupdate 测试覆盖。

```sh
SINGBOX_MUSL_BIN=/path/to/sing-box MITA_BIN=/path/to/mita MIERU_BIN=/path/to/mieru ALPINE_TEST_CASE=mita ALPINE_TEST_MEMORY=512m bash scripts/test-alpine-container.sh
SINGBOX_MUSL_BIN=/path/to/sing-box MAINTENANCE_INIT=openrc bash scripts/test-maintenance-container.sh
SINGBOX_MUSL_BIN=/path/to/sing-box MAINTENANCE_INIT=openrc ALPINE_TEST_PURGE=1 bash scripts/test-maintenance-container.sh
```

既有夹具默认使用 privileged 容器。可通过 `ALPINE_TEST_SECCOMP=/absolute/path/profile.json` 使用明确能力列表替代：SYS_ADMIN、NET_ADMIN、SYS_RESOURCE、SYS_PTRACE、BPF、PERFMON。该 profile 应保留 Docker 默认限制并允许 bubblewrap 的 pivot_root；不挂载宿主 cgroup、Docker socket 或可写源码。正式 agent 的非 root 沙箱和 seccomp 不因此改变。

## 1.4 真实安装脚本与终端更新

```sh
bash scripts/test-alpine-installer-container.sh
```

独立 256 MiB 容器运行未经改写的生产安装脚本、真实 OpenRC 服务与 agent。临时 HTTPS 镜像提供本地编译的两个测试版本、SHA256SUMS、验证器和卸载器；只有容器信任临时证书并将 GitHub 域名解析到回环地址。包依赖在镜像构建时真实安装，运行时清空容器包仓库列表，以已安装依赖执行实际 apk 命令，不模拟 apk 或服务命令。它验证 HTTPS 与摘要流程，不能证明候选已在公开 GitHub 发布。

临时目录明确挂载为 `noexec`，验证首次安装仍可完成预检、一次性注册、开机启用、真实进程就绪与鉴权心跳。安装结束后重新获取安装锁，确认后台服务未导致锁永久占用。随后破坏下载内容，确认摘要拒绝且原进程与注册身份不变；再执行正常跨版本终端更新，验证新进程就绪、注册身份保留、不重复注册、心跳恢复及停服后旧就绪记录失效。ARM64 已实际通过且无 OOM，原生 AMD64 验证列入兼容 CI。
