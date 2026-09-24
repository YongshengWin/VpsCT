#!/usr/bin/env bash
# Uninstall the default systemd layout, including installations made by v0.1.0.
set -euo pipefail

ROLE='' PURGE=0 YES=0 DRY_RUN=0 REMOVE_CADDY=0
ROOT='' # Only changed by sourced, isolated tests; never read from the environment.
UNITS=() REMOVE=() KEEP=()
NFT_PRESENT=0 NFT_NODES_PRESENT=0 NFT_EGRESS_PRESENT=0 NFT_NETWORK_PRESENT=0 NFT_FORWARDS_PRESENT=0 NFT_ADMISSION_PRESENT=0 CADDY_DOMAIN=''
NFT_INGRESS_HANDLES=()

die() { printf '错误：%s\n' "$*" >&2; exit 1; }
info() { printf '==> %s\n' "$*"; }
path() { printf '%s%s' "$ROOT" "$1"; }
controller() { [[ "$ROLE" == controller || "$ROLE" == all ]]; }
agent() { [[ "$ROLE" == agent || "$ROLE" == all ]]; }
usage() {
  cat <<'EOF'
VpsCT 卸载器（Linux / systemd 默认安装路径，兼容 v0.1.0）

  sudo bash uninstall.sh --controller       卸载控制端，保留数据和环境配置
  sudo bash uninstall.sh --agent            卸载 agent 及其部署的服务，保留配置和数据
  sudo bash uninstall.sh --all              卸载本机两端

选项：
  --purge          同时删除所选端的配置、数据、凭据、日志和默认目录内的备份
  --remove-caddy   停用 Caddy，清理独占站点配置和证书（需选择控制端并加 --purge）
  --dry-run        只检查并列出计划，不停止服务或删除文件
  --yes            跳过交互确认；管道运行时必须指定
  --help           显示帮助

示例：sudo bash uninstall.sh --all --purge --remove-caddy --yes
不会删除另一端、远端服务器、面板记录、系统通用软件包或自行另存的备份。
Caddy 软件包、全局运行数据和 ctlvps 系统账户保留；共享或改写过的 Caddy 配置需手动处理。
Docker、自定义程序/数据路径、systemd 覆盖配置不在自动卸载范围内。
EOF
}

parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --controller|--agent|--all)
        [[ -z "$ROLE" ]] || die '只能选择 --controller、--agent、--all 中的一项'
        ROLE=${1#--} ;;
      --purge) PURGE=1 ;;
      --yes) YES=1 ;;
      --dry-run) DRY_RUN=1 ;;
      --remove-caddy) REMOVE_CADDY=1 ;;
      *) die "未知参数：$1" ;;
    esac
    shift
  done
  [[ -n "$ROLE" ]] || die '必须指定 --controller、--agent 或 --all'
  [[ "$REMOVE_CADDY" == 0 ]] || controller || die '--remove-caddy 只能与控制端卸载一起使用'
  [[ "$REMOVE_CADDY" == 0 || "$PURGE" == 1 ]] || die '--remove-caddy 会删除站点配置与证书，需同时指定 --purge'
}

# Never follow parent symlinks or traverse mounted filesystems during deletion.
# A symlink at the selected leaf is unlinked, not followed.
safe_path() {
  local target=$1 parent mounts mount
  [[ "$target" == "$ROOT"/opt/ctlvps/* || "$target" == "$ROOT"/etc/ctlvps/* ||
     "$target" == "$ROOT"/etc/ctlvps-proxy ||
     "$target" == "$ROOT"/etc/systemd/system/ctlvps-proxy-guard.timer ||
     "$target" == "$ROOT"/etc/systemd/system/ctlvps*.service ||
     "$target" == "$ROOT"/etc/systemd/system/ctlvps-proxy.slice ||
     "$target" =~ ^"$ROOT"/etc/systemd/system/ctlvps-proxy-(n[0-9]+|public|private)\.slice$ ||
     "$target" =~ ^"$ROOT"/etc/systemd/system/ctlvps-(snell|mita)@[0-9]+\.service\.d/meter\.conf$ ||
     "$target" == "$ROOT"/run/ctlvps-proxy ||
     "$target" == "$ROOT"/usr/local/bin/ctlvps-agent ||
     "$target" == "$ROOT"/usr/local/libexec/ctlvps-agent-uninstall.sh ||
     "$target" == "$ROOT"/var/lib/ctlvps-agent || "$target" == "$ROOT"/var/lib/ctlvps-proxy || "$target" == "$ROOT"/var/log/ctlvps ||
     "$target" == "$ROOT"/etc/caddy/Caddyfile ||
     "$target" == "$ROOT"/var/lib/caddy/.local/share/caddy/certificates/* ]] || die '拒绝清理范围外的路径'
  parent=${target%/*}
  while [[ "$parent" != "$ROOT" && -n "$parent" ]]; do
    [[ ! -L "$parent" ]] || die "父目录为软链接，需手动处理：$parent"
    parent=${parent%/*}
  done
  [[ ! -L "$target" ]] || return 0
  mounts=$(findmnt -rn -o TARGET) || die '无法检查挂载点'
  while IFS= read -r mount; do
    [[ "$mount" != "$target" && "$mount" != "$target/"* ]] || die "清理范围内存在挂载点：$target"
  done <<< "$mounts"
}

add_remove() {
  local existing
  for existing in "${REMOVE[@]}"; do [[ "$existing" != "$1" ]] || return 0; done
  safe_path "$1"; REMOVE+=("$1")
}
data_path() {
  if [[ "$PURGE" == 1 ]]; then add_remove "$1"; else KEEP+=("$1"); fi
}

add_unit() {
  local unit=$1 expected=${2:-} state fragment dropins start existing content pattern
  for existing in "${UNITS[@]}"; do [[ "$existing" != "$unit" ]] || return 0; done
  [[ "$unit" =~ ^ctlvps(d|-agent|-maintenance|-proxy-guard|-singbox(-private|@[0-9]*)?|-(snell|mita)@[0-9]*)\.service$ || "$unit" == ctlvps-proxy-guard.timer || "$unit" =~ ^ctlvps-proxy(-(n[0-9]+|public|private))?\.slice$ || "$unit" == caddy.service ]] || die '服务名超出卸载范围'
  state=$(systemctl show "$unit" -p LoadState --value) || die "无法读取服务：$unit"
  [[ "$state" != not-found ]] || return 0
  [[ "$state" == loaded || "$state" == masked ]] || die "服务状态异常：$unit"
  fragment=$(systemctl show "$unit" -p FragmentPath --value) || die "无法读取服务路径：$unit"
  dropins=$(systemctl show "$unit" -p DropInPaths --value) || die "无法读取服务覆盖配置：$unit"
  if [[ -n "$dropins" ]]; then
    [[ "$unit" =~ ^ctlvps-(snell|mita)@[0-9]+\.service$ && "$dropins" == "/etc/systemd/system/$unit.d/meter.conf" ]] || die "$unit 有自定义 systemd 覆盖配置，请手动处理"
    content=$(cat "$(path "$dropins")") || die '无法读取节点计量配置'
    pattern=$'^\[Service\]\nSlice=ctlvps-proxy-n[0-9]+\.slice$'
    [[ "$content" =~ $pattern ]] || die '节点计量覆盖配置不是安装器生成的内容'
    add_remove "$(path "$dropins")"
  fi
  if [[ "$unit" != caddy.service ]]; then
    [[ "$fragment" == /etc/systemd/system/ctlvps*.service || "$fragment" == /etc/systemd/system/ctlvps-proxy-guard.timer || "$fragment" == /etc/systemd/system/ctlvps-proxy*.slice || "$fragment" == /dev/null ]] || die "$unit 不在默认服务目录"
  fi
  if [[ -n "$expected" && "$state" != masked ]]; then
    start=$(systemctl show "$unit" -p ExecStart --value) || die "无法读取启动命令：$unit"
    case "$unit" in
      ctlvps-singbox.service) [[ "$start" != *"argv[]=/usr/local/bin/ctlvps-agent proxy-exec singbox run public ;"* ]] || expected='/usr/local/bin/ctlvps-agent proxy-exec singbox run public' ;;
      ctlvps-snell@*) [[ "$start" != *"argv[]=/usr/local/bin/ctlvps-agent proxy-exec snell run ${unit#*@}"* ]] || :
        local proxy_port=${unit#*@}; proxy_port=${proxy_port%.service}
        [[ "$start" != *"argv[]=/usr/local/bin/ctlvps-agent proxy-exec snell run $proxy_port ;"* ]] || expected="/usr/local/bin/ctlvps-agent proxy-exec snell run $proxy_port" ;;
    esac
    [[ "$start" == *"argv[]=$expected ;"* ]] || die "$unit 使用自定义启动参数或路径，请手动处理"
  fi
  UNITS+=("$unit")
}

plan_agent() {
  local listed files status unit rest file name expected port
  add_unit ctlvps-proxy-guard.timer
  add_unit ctlvps-proxy-guard.service '/usr/local/bin/ctlvps-agent proxy-guard'
  add_unit ctlvps-agent.service '/usr/local/bin/ctlvps-agent run --state /var/lib/ctlvps-agent'
  listed=$(systemctl list-units --all --plain --no-legend --no-pager 'ctlvps-singbox*' 'ctlvps-snell*' 'ctlvps-mita*') || die '无法枚举节点服务'
  if files=$(systemctl list-unit-files --no-legend --no-pager 'ctlvps-singbox*' 'ctlvps-snell*' 'ctlvps-mita*'); then
    :
  else
    status=$?
    # systemctl returns 1 (with no output) when no matching unit files remain.
    [[ "$status" == 1 && -z "$files" ]] || die '无法枚举节点服务文件'
  fi
  listed+=$'\n'"$files"
  while read -r unit rest; do
    [[ -n "$unit" ]] || continue
    [[ "$unit" =~ ^ctlvps-(singbox|snell|mita)(-private|@[0-9]*)?\.service$ ]] || die "发现非标准节点服务：$unit"
    # Templates themselves have no running process.
    [[ "$unit" != *@.service ]] || continue
    port=${unit#*@}; port=${port%.service}
    case "$unit" in
      ctlvps-singbox-private.service) expected='/usr/local/bin/ctlvps-agent proxy-exec singbox run private' ;;
      ctlvps-singbox.service) expected='/opt/ctlvps/bin/sing-box run -c /etc/ctlvps/sing-box.json' ;;
      ctlvps-singbox@*) expected="/opt/ctlvps/bin/sing-box run -c /etc/ctlvps/sing-box/$port.json" ;;
      ctlvps-mita@*) expected="/usr/local/bin/ctlvps-agent proxy-exec mieru run $port" ;;
      ctlvps-snell@*) expected="/opt/ctlvps/bin/snell-server -c /etc/ctlvps/snell/$port.conf" ;;
    esac
    add_unit "$unit" "$expected"
  done <<< "$listed"
  for name in ctlvps-proxy-guard.timer ctlvps-proxy-guard.service ctlvps-proxy.slice ctlvps-agent.service ctlvps-singbox.service ctlvps-singbox-private.service ctlvps-singbox@.service ctlvps-snell@.service ctlvps-mita@.service; do
    add_remove "$(path /etc/systemd/system)/$name"
  done
  for file in "$(path /etc/systemd/system)"/ctlvps-singbox@*.service "$(path /etc/systemd/system)"/ctlvps-snell@*.service "$(path /etc/systemd/system)"/ctlvps-mita@*.service; do
    [[ -e "$file" || -L "$file" ]] || continue
    name=${file##*/}
    [[ "$name" =~ ^ctlvps-(singbox|snell|mita)@[0-9]*\.service$ ]] || die "发现非标准节点服务文件：$name"
    add_remove "$file"
  done
  for file in "$(path /etc/systemd/system)"/ctlvps-proxy*.slice; do
    [[ -e "$file" || -L "$file" ]] || continue
    name=${file##*/}
    [[ "$name" =~ ^ctlvps-proxy(-(n[0-9]+|public|private))?\.slice$ ]] || die '非标准代理资源 slice'
    add_unit "$name"
    add_remove "$file"
  done
  add_remove "$(path /run/ctlvps-proxy)"
  add_remove "$(path /usr/local/bin/ctlvps-agent)"
  if [[ "$PURGE" -eq 1 ]]; then add_remove "$(path /usr/local/libexec/ctlvps-agent-uninstall.sh)"; fi
  for name in sing-box snell-server mita; do
    for file in "$name" "$name.version" "$name.trusted" "$name.tmp"; do add_remove "$(path /opt/ctlvps/bin)/$file"; done
  done
  for file in /etc/ctlvps/sing-box /etc/ctlvps/sing-box.json /etc/ctlvps/snell /etc/ctlvps/mita /etc/ctlvps/proxy /etc/ctlvps-proxy /var/lib/ctlvps-proxy /var/lib/ctlvps-agent /var/log/ctlvps; do
    data_path "$(path "$file")"
  done
  if command -v nft >/dev/null; then
    listed=$(nft list tables) || die '无法检查 nftables，尚未停服或删除文件'
    while read -r kind family table extra; do
      [[ "$kind" == table && "$family" == inet && -z "$extra" ]] || continue
      [[ "$table" != ctlvps ]] || NFT_PRESENT=1
      [[ "$table" != ctlvps_nodes ]] || NFT_NODES_PRESENT=1
      [[ "$table" != ctlvps_egress ]] || NFT_EGRESS_PRESENT=1
      [[ "$table" != ctlvps_network ]] || NFT_NETWORK_PRESENT=1
      [[ "$table" != ctlvps_forwards ]] || NFT_FORWARDS_PRESENT=1
      [[ "$table" != ctlvps_forward_admission ]] || NFT_ADMISSION_PRESENT=1
    done <<< "$listed"
    local ingress line handle
    if ingress=$(nft -a list chain inet filter input 2>/dev/null); then
      while IFS= read -r line; do
        [[ "$line" == *'comment "ctlvps-node-ingress:'* ]] || continue
        handle=${line##*# handle }
        [[ "$handle" =~ ^[0-9]+$ ]] || die '节点端口规则句柄无效'
        NFT_INGRESS_HANDLES+=("$handle")
      done <<< "$ingress"
    fi
  fi
}

plan_controller() {
  local file data
  add_unit ctlvpsd.service '/opt/ctlvps/ctlvpsd'
  add_unit ctlvps-maintenance.service '/opt/ctlvps/ctlvpsd maintenance-serve'
  file=$(path /etc/ctlvps/ctlvpsd.env)
  safe_path "$file"
  if [[ "$PURGE" == 1 && -f "$file" && ! -L "$file" ]]; then
    data=$(sed -n 's/^CTLVPS_DATA_DIR=//p' "$file")
    [[ -z "$data" || "$data" == /opt/ctlvps/data ]] || die '控制端使用自定义数据目录，请手动处理'
  fi
  for file in /etc/systemd/system/ctlvpsd.service /etc/systemd/system/ctlvps-maintenance.service /opt/ctlvps/current /opt/ctlvps/ctlvpsd /opt/ctlvps/agents /opt/ctlvps/uninstall.sh /opt/ctlvps/releases /opt/ctlvps/REPOSITORY; do
    add_remove "$(path "$file")"
  done
  for file in /etc/ctlvps/ctlvpsd.env /etc/ctlvps/secrets.key /opt/ctlvps/data /opt/ctlvps/backups; do data_path "$(path "$file")"; done
}

plan_caddy() {
  local file compact expected issuer cert
  [[ "$REMOVE_CADDY" == 1 ]] || return 0
  file=$(path /etc/caddy/Caddyfile)
  safe_path "$file"
  if [[ ! -e "$file" && ! -L "$file" ]]; then info '没有 Caddyfile，跳过 HTTPS 站点清理'; return; fi
  [[ -f "$file" && ! -L "$file" ]] || die 'Caddyfile 不是普通文件'
  # Accept only the exact single-site layout created by install.sh, whitespace aside.
  [[ "$(head -n 1 "$file")" == '# Managed by VpsCT installer' ]] || die 'Caddy 配置不属于安装器，请去掉 --remove-caddy 后重试'
  CADDY_DOMAIN=$(sed -n '2s/[[:space:]]*{.*//p' "$file")
  [[ "$CADDY_DOMAIN" =~ ^[A-Za-z0-9][A-Za-z0-9.-]+$ && "$CADDY_DOMAIN" != *..* ]] || die '无法识别 Caddy 站点'
  compact=$(tr -d '[:space:]' < "$file")
  expected="#ManagedbyVpsCTinstaller$CADDY_DOMAIN{reverse_proxy127.0.0.1:8080}"
  [[ "$compact" == "$expected" ]] || die 'Caddy 配置已修改或包含其他站点，请手动处理 HTTPS 配置'
  add_unit caddy.service '/usr/bin/caddy run --environ --config /etc/caddy/Caddyfile'
  add_remove "$file"
  if [[ "$PURGE" == 1 ]]; then
    # Remove only this domain's certificates, never Caddy's shared data tree.
    for issuer in "$(path /var/lib/caddy/.local/share/caddy/certificates)"/*; do
      [[ -d "$issuer" ]] || continue
      cert="$issuer/${CADDY_DOMAIN,,}"
      [[ -e "$cert" || -L "$cert" ]] || continue
      add_remove "$cert"
    done
  fi
}

plan() {
  UNITS=() REMOVE=() KEEP=() NFT_PRESENT=0 NFT_NODES_PRESENT=0 NFT_EGRESS_PRESENT=0 NFT_NETWORK_PRESENT=0
  NFT_INGRESS_HANDLES=()
  agent && plan_agent
  controller && plan_controller
  plan_caddy
  info "卸载范围：$ROLE；删除数据：$PURGE；移除独占 HTTPS 站点：$REMOVE_CADDY"
  if [[ ${#UNITS[@]} -gt 0 ]]; then printf '停止并禁用：\n'; printf '  %s\n' "${UNITS[@]}"; fi
  printf '删除（不存在的路径将跳过）：\n'; printf '  %s\n' "${REMOVE[@]}"
  if [[ ${#KEEP[@]} -gt 0 ]]; then printf '保留：\n'; printf '  %s\n' "${KEEP[@]}"; fi
  [[ ${#NFT_INGRESS_HANDLES[@]} == 0 ]] || printf '删除 VpsCT 自动开放的节点端口规则（保留其他入站规则）\n'
  [[ "$NFT_EGRESS_PRESENT" == 0 ]] || printf "删除 nftables 表：inet ctlvps_egress\n"
  [[ "$NFT_NETWORK_PRESENT" == 0 ]] || printf '删除 nftables 表：inet ctlvps_network（节点网络绑定保护）\n'
  [[ "$NFT_FORWARDS_PRESENT" == 0 ]] || printf '删除 nftables 表：inet ctlvps_forwards（固定转发计量）\n'
  [[ "$NFT_ADMISSION_PRESENT" == 0 ]] || printf '删除 nftables 表：inet ctlvps_forward_admission（固定转发连接上限）\n'
  [[ "$NFT_NODES_PRESENT" == 0 ]] || printf '删除 nftables 表：inet ctlvps_nodes（不修改其他表）\n'
  [[ "$NFT_PRESENT" == 0 ]] || printf '删除 nftables 表：inet ctlvps（不修改其他表）\n'
  return 0
}

execute_plan() {
  local unit file before handle
  # Stop all services successfully before deleting anything. Agent must go first
  # so it cannot recreate core services or re-download their executables.
  for unit in "${UNITS[@]}"; do
    before=$(systemctl show "$unit" -p ActiveState --value) || die "无法读取 $unit 运行状态"
    systemctl stop "$unit" || die "停止 $unit 失败，尚未删除文件"
    if systemctl is-active --quiet "$unit"; then die "$unit 仍在运行，尚未删除文件"; fi
    if [[ "$before" == active || "$before" == activating || "$before" == deactivating ]] && systemctl is-failed --quiet "$unit"; then
      die "$unit 停止过程中发生错误，尚未删除文件"
    fi
    systemctl disable "$unit" || die "禁用 $unit 失败，尚未删除文件"
  done
  for handle in "${NFT_INGRESS_HANDLES[@]}"; do
    nft delete rule inet filter input handle "$handle" || die "清理节点端口规则失败"
  done
  [[ "$NFT_EGRESS_PRESENT" == 0 ]] || nft delete table inet ctlvps_egress || die "清理代理出站表失败"
  [[ "$NFT_NETWORK_PRESENT" == 0 ]] || nft delete table inet ctlvps_network || die '清理节点网络绑定表失败，尚未删除文件'
  [[ "$NFT_FORWARDS_PRESENT" == 0 ]] || nft delete table inet ctlvps_forwards || die '清理固定转发计量表失败，尚未删除文件'
  [[ "$NFT_ADMISSION_PRESENT" == 0 ]] || nft delete table inet ctlvps_forward_admission || die '清理固定转发准入表失败，尚未删除文件'
  [[ "$NFT_NODES_PRESENT" == 0 ]] || nft delete table inet ctlvps_nodes || die '清理节点计量表失败，尚未删除文件'
  [[ "$NFT_PRESENT" == 0 ]] || nft delete table inet ctlvps || die '清理项目计量表失败，尚未删除文件'
  for file in "${REMOVE[@]}"; do
    safe_path "$file"
    rm -rf --one-file-system -- "$file"
  done
  # Shared parent directories are removed only if empty.
  for file in /opt/ctlvps/bin /opt/ctlvps /etc/ctlvps; do rmdir -- "$(path "$file")" 2>/dev/null || true; done
  systemctl daemon-reload
  info '所选端的卸载已完成'
  [[ "$PURGE" == 1 ]] || info '配置与数据已保留；如需清空，可用相同角色加 --purge 再运行一次'
  if controller && [[ "$REMOVE_CADDY" == 0 ]]; then info 'HTTPS 配置未处理；安装器生成的独占 Caddy 站点可另加 --remove-caddy 清理'; fi
  info '系统账户、通用软件包、Caddy 全局缓存、另存的备份及远端/面板记录不在删除范围内'
}

main() {
  if [[ "$*" == --help || "$*" == -h ]]; then usage; return; fi
  parse_args "$@"
  [[ "$(uname -s)" == Linux && "$(id -u)" == 0 ]] || die '请在 Linux 上使用 root 或 sudo 运行'
  [[ -d /run/systemd/system ]] || die '需要正在运行的 systemd；不用于 Docker 部署'
  local tool answer
  for tool in systemctl findmnt flock rm sed head tr; do command -v "$tool" >/dev/null || die "缺少依赖：$tool"; done
  umask 077
  # Same lock as the controller installer; a dry run deliberately writes nothing.
  if [[ "$DRY_RUN" == 0 ]]; then
    exec 9>/run/lock/ctlvps-install.lock
    flock -n 9 || die '有安装、更新或卸载任务正在运行'
  fi
  plan
  [[ "$DRY_RUN" == 0 ]] || { info '仅预览，未执行卸载'; return; }
  if [[ "$YES" == 0 ]]; then
    [[ -t 0 ]] || die '非交互运行需要 --yes；可先用 --dry-run 查看范围'
    if [[ "$PURGE" == 1 ]]; then printf '配置和数据将永久删除。'; fi
    read -r -p '输入 uninstall 确认卸载：' answer
    [[ "$answer" == uninstall ]] || die '已取消'
  fi
  execute_plan
}

if [[ "${BASH_SOURCE[0]:-$0}" == "$0" ]]; then main "$@"; fi
