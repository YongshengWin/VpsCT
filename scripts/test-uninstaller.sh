#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
# shellcheck source=uninstall.sh
source ./uninstall.sh
reject_args() {
  if (ROLE=''; parse_args "$@") >/dev/null 2>&1; then
    printf 'Invalid uninstall arguments accepted\n' >&2; exit 1
  fi
}
reject_args
reject_args --agent --controller
reject_args --all --agent
reject_args --agent --remove-caddy
reject_args --controller --remove-caddy
reject_args --purge
reject_args --controller --unknown
(ROLE=''; parse_args --all --purge --remove-caddy --dry-run --yes)
bash uninstall.sh --help >/dev/null

# Check NAT cleanup without touching the host's services, firewall or paths.
(
  test_root=$(mktemp -d)
  trap 'command rm -rf -- "$test_root"' EXIT
  ROOT=$test_root
  nat_config="$ROOT/etc/ctlvps/agent-nat.json"
  nft_log="$ROOT/nft-actions"
  mkdir -p "$ROOT/etc/ctlvps"
  printf '{"schema":1,"mappings":[]}\n' > "$nat_config"
  systemctl() {
    case "$1" in
      show) printf 'not-found\n' ;;
      list-units|daemon-reload) return 0 ;;
      list-unit-files) return 1 ;;
      *) printf 'Unexpected systemctl mutation\n' >&2; return 1 ;;
    esac
  }
  findmnt() { return 0; }
  nft() {
    case "$*" in
      'list tables') printf 'table inet ctlvps_nat\ntable ip ctlvps_nat\ntable inet unrelated_nat\n' ;;
      '-a list chain inet filter input') return 1 ;;
      'delete table inet ctlvps_nat') printf '%s\n' "$*" >> "$nft_log" ;;
      *) printf 'Unexpected nftables mutation\n' >&2; return 1 ;;
    esac
  }
  # GNU rm's mount guard is checked by safe_path above; use the portable rm
  # only inside this test root so the assertion also runs on macOS.
  rm() {
    local target=${@: -1}
    [[ "$target" == "$ROOT/"* ]] || return 1
    command rm -rf -- "$target"
  }

  ROLE=agent PURGE=0 REMOVE_CADDY=0 DRY_RUN=1
  plan > "$ROOT/agent-plan"
  [[ "$NFT_NAT_PRESENT" == 1 ]]
  [[ " ${KEEP[*]} " == *" $nat_config "* ]]
  [[ " ${REMOVE[*]} " != *" $nat_config "* ]]
  [[ "$(cat "$ROOT/agent-plan")" == *'inet ctlvps_nat（本机 NAT 端口转换）'* ]]
  [[ -f "$nat_config" && ! -e "$nft_log" ]]
  execute_plan > /dev/null
  [[ -f "$nat_config" ]]
  [[ "$(cat "$nft_log")" == 'delete table inet ctlvps_nat' ]]

  ROLE=controller PURGE=1
  plan > "$ROOT/controller-plan"
  [[ "$NFT_NAT_PRESENT" == 0 ]]
  [[ " ${REMOVE[*]} " != *" $nat_config "* ]]
  [[ "$(cat "$ROOT/controller-plan")" != *'inet ctlvps_nat'* ]]
  execute_plan > /dev/null
  [[ -f "$nat_config" ]]
  [[ "$(cat "$nft_log")" == 'delete table inet ctlvps_nat' ]]

  ROLE=agent PURGE=1
  plan > "$ROOT/purge-plan"
  [[ "$NFT_NAT_PRESENT" == 1 ]]
  [[ " ${REMOVE[*]} " == *" $nat_config "* ]]
  [[ -f "$nat_config" ]]
  execute_plan > /dev/null
  [[ ! -e "$nat_config" ]]
  [[ "$(cat "$nft_log")" == $'delete table inet ctlvps_nat\ndelete table inet ctlvps_nat' ]]
)

(
  test_root=$(mktemp -d)
  trap 'command rm -rf -- "$test_root"' EXIT
  ROOT=$test_root INIT=openrc ROLE=agent PURGE=0 REMOVE_CADDY=0
  export VPSCT_TEST_ROOT=$test_root
  mkdir -p "$ROOT/etc/init.d" "$ROOT/etc/ctlvps" "$ROOT/usr/local/bin" "$ROOT/var/lib/ctlvps-agent"
  printf 'managed agent\n' > "$ROOT/etc/init.d/ctlvps-agent"
  printf 'managed core\n' > "$ROOT/etc/init.d/ctlvps-singbox"
  printf 'managed private core\n' > "$ROOT/etc/init.d/ctlvps-singbox-private"
  printf 'managed guard\n' > "$ROOT/etc/init.d/ctlvps-proxy-guard"
  printf 'managed service host\n' > "$ROOT/etc/init.d/ctlvps-service-host"
  printf 'unrelated service\n' > "$ROOT/etc/init.d/ctlvpsd"
  printf '{"schema":1,"mappings":[]}\n' > "$ROOT/etc/ctlvps/agent-nat.json"
  printf 'retained state\n' > "$ROOT/var/lib/ctlvps-agent/state.json"
  test_group() {
    case "$1" in
      ctlvps.slice) printf '%s/sys/fs/cgroup/ctlvps.slice' "$ROOT" ;;
      ctlvps-singbox.service) printf '%s/sys/fs/cgroup/ctlvps.slice/ctlvps-proxy.slice/ctlvps-proxy-public.slice/%s' "$ROOT" "$1" ;;
      ctlvps-singbox-private.service) printf '%s/sys/fs/cgroup/ctlvps.slice/ctlvps-proxy.slice/ctlvps-proxy-private.slice/%s' "$ROOT" "$1" ;;
      ctlvps-proxy.slice) printf '%s/sys/fs/cgroup/ctlvps.slice/%s' "$ROOT" "$1" ;;
      ctlvps-proxy-public.slice|ctlvps-proxy-private.slice) printf '%s/sys/fs/cgroup/ctlvps.slice/ctlvps-proxy.slice/%s' "$ROOT" "$1" ;;
      *) printf '%s/sys/fs/cgroup/ctlvps.slice/%s' "$ROOT" "$1" ;;
    esac
  }
  test_population() {
    local group
    group=$(test_group "$1")
    mkdir -p "$group"
    printf 'populated %s\nfrozen 0\n' "$2" > "$group/cgroup.events"
  }
  reset_populations() {
    local unit
    for unit in ctlvps-agent.service ctlvps-proxy-guard.service ctlvps-singbox.service ctlvps-singbox-private.service ctlvps-service-host.service ctlvps-proxy.slice ctlvps-proxy-public.slice ctlvps-proxy-private.slice ctlvps.slice; do
      test_population "$unit" 1
    done
    test_population ctlvps-proxy-check-public.service 0
    test_population ctlvps-proxy-check-private.service 0
  }
  clear_actions() {
    command rm -f -- "$ROOT/service-actions" "$ROOT/cgroup-actions" "$ROOT/runlevel-actions" "$ROOT/nft-actions"
  }
  reset_populations
  cat > "$ROOT/usr/local/bin/ctlvps-agent" <<'EOF'
#!/bin/sh
group() {
  case "$1" in
    ctlvps.slice) printf '%s/sys/fs/cgroup/ctlvps.slice' "$VPSCT_TEST_ROOT" ;;
    ctlvps-singbox.service) printf '%s/sys/fs/cgroup/ctlvps.slice/ctlvps-proxy.slice/ctlvps-proxy-public.slice/%s' "$VPSCT_TEST_ROOT" "$1" ;;
    ctlvps-singbox-private.service) printf '%s/sys/fs/cgroup/ctlvps.slice/ctlvps-proxy.slice/ctlvps-proxy-private.slice/%s' "$VPSCT_TEST_ROOT" "$1" ;;
    ctlvps-proxy-public.slice|ctlvps-proxy-private.slice) printf '%s/sys/fs/cgroup/ctlvps.slice/ctlvps-proxy.slice/%s' "$VPSCT_TEST_ROOT" "$1" ;;
    *) printf '%s/sys/fs/cgroup/ctlvps.slice/%s' "$VPSCT_TEST_ROOT" "$1" ;;
  esac
}
empty() {
  [ "$1" != "${VPSCT_TEST_STICKY_UNIT:-}" ] || return 0
  printf 'populated 0\nfrozen 0\n' > "$(group "$1")/cgroup.events"
}
case "$1" in
  openrc-verify)
    case "$2" in
      ctlvps-agent.service) expected='managed agent' ;;
      ctlvps-proxy-guard.service) expected='managed guard' ;;
      ctlvps-singbox.service) expected='managed core' ;;
      ctlvps-singbox-private.service) expected='managed private core' ;;
      ctlvps-service-host.service) expected='managed service host' ;;
      *) exit 1 ;;
    esac
    content=$(cat "$VPSCT_TEST_ROOT/etc/init.d/${2%.service}") || exit 1
    [ "$content" = "$expected" ] && test ! -f "$VPSCT_TEST_ROOT/reject-service" ;;
  openrc-stop)
    printf '%s\n' "$*" >> "$VPSCT_TEST_ROOT/cgroup-actions"
    empty "$2"
    if [ "$2" = ctlvps-service-host.service ]; then
      for unit in ctlvps-proxy-public.slice ctlvps-proxy-private.slice ctlvps-proxy.slice ctlvps.slice; do empty "$unit"; done
    fi ;;
  *) exit 1 ;;
esac
EOF
  chmod 0755 "$ROOT/usr/local/bin/ctlvps-agent"
  systemctl() { printf 'Unexpected systemd operation\n' >&2; return 1; }
  findmnt() { return 0; }
  rc-service() {
    printf '%s\n' "$*" >> "$ROOT/service-actions"
    case "$2" in
      stop) [[ "${VPSCT_STOP_FAIL:-0}" == 0 ]] ;;
      status) return 1 ;;
      *) return 1 ;;
    esac
  }
  rc-update() { printf '%s\n' "$*" >> "$ROOT/runlevel-actions"; }
  nft() {
    case "$*" in
      'list tables') printf 'table inet ctlvps_nat\n' ;;
      '-a list chain inet filter input') return 1 ;;
      'delete table inet ctlvps_nat') printf '%s\n' "$*" >> "$ROOT/nft-actions" ;;
      *) return 1 ;;
    esac
  }
  rm() {
    local target=${@: -1}
    [[ "$target" == "$ROOT/"* ]] || return 1
    command rm -rf -- "$target"
  }
  plan > "$ROOT/plan"
  [[ "${UNITS[*]}" == 'ctlvps-agent.service ctlvps-proxy-guard.service ctlvps-singbox.service ctlvps-singbox-private.service ctlvps-service-host.service' ]]
  [[ " ${UNITS[*]} " != *'ctlvpsd'* ]]
  [[ ! -e "$ROOT/service-actions" && ! -e "$ROOT/nft-actions" ]]
  touch "$ROOT/reject-service"
  if (plan) > /dev/null 2>&1; then printf 'Modified OpenRC service accepted\n' >&2; exit 1; fi
  [[ ! -e "$ROOT/service-actions" ]]
  command rm "$ROOT/reject-service"
  # A script modified after planning is reverified before rc-service sees it.
  plan > /dev/null
  printf '#!/bin/sh\ntouch "$VPSCT_TEST_ROOT/bad-script-ran"\n' > "$ROOT/etc/init.d/ctlvps-proxy-guard"
  if (execute_plan) > /dev/null 2>&1; then printf 'Late modified OpenRC service accepted\n' >&2; exit 1; fi
  [[ "$(cat "$ROOT/service-actions")" == $'ctlvps-agent stop\nctlvps-agent status' ]]
  [[ ! -e "$ROOT/bad-script-ran" && ! -e "$ROOT/nft-actions" && -f "$ROOT/usr/local/bin/ctlvps-agent" ]]
  printf 'managed guard\n' > "$ROOT/etc/init.d/ctlvps-proxy-guard"
  clear_actions
  reset_populations
  # An orphaned business cgroup must be rejected without running a service.
  command rm "$ROOT/etc/init.d/ctlvps-singbox-private"
  if (plan) > /dev/null 2>&1; then printf 'Orphaned OpenRC cgroup accepted\n' >&2; exit 1; fi
  [[ ! -e "$ROOT/service-actions" && ! -e "$ROOT/nft-actions" ]]
  [[ -f "$ROOT/usr/local/bin/ctlvps-agent" && -f "$ROOT/var/lib/ctlvps-agent/state.json" ]]
  printf 'managed private core\n' > "$ROOT/etc/init.d/ctlvps-singbox-private"
  plan > /dev/null
  if (VPSCT_STOP_FAIL=1; execute_plan) > /dev/null 2>&1; then printf 'Failed OpenRC stop accepted\n' >&2; exit 1; fi
  [[ -f "$ROOT/etc/init.d/ctlvps-agent" && ! -e "$ROOT/nft-actions" ]]
  # Even a successful stop command is insufficient if a fixed leaf, temporary
  # preflight or descendant worker remains populated. Purge must retain data,
  # the launcher and firewall rules in each failure case.
  PURGE=1
  for sticky in ctlvps-singbox.service ctlvps-proxy-check-private.service ctlvps.slice; do
    clear_actions
    reset_populations
    if [[ "$sticky" == ctlvps-proxy-check-private.service ]]; then test_population "$sticky" 1; fi
    plan > /dev/null
    if (export VPSCT_TEST_STICKY_UNIT=$sticky; execute_plan) > /dev/null 2>&1; then printf 'Populated OpenRC cgroup accepted after stop\n' >&2; exit 1; fi
    [[ -f "$ROOT/usr/local/bin/ctlvps-agent" && -f "$ROOT/etc/init.d/ctlvps-agent" ]]
    [[ -f "$ROOT/var/lib/ctlvps-agent/state.json" && -f "$ROOT/etc/ctlvps/agent-nat.json" && ! -e "$ROOT/nft-actions" ]]
  done
  # Detect a missing-script cgroup that becomes populated after prechecking.
  clear_actions
  reset_populations
  command rm "$ROOT/etc/init.d/ctlvps-singbox-private"
  test_population ctlvps-singbox-private.service 0
  plan > /dev/null
  test_population ctlvps-singbox-private.service 1
  if (execute_plan) > /dev/null 2>&1; then printf 'Late orphaned OpenRC cgroup accepted\n' >&2; exit 1; fi
  [[ -f "$ROOT/usr/local/bin/ctlvps-agent" && -f "$ROOT/var/lib/ctlvps-agent/state.json" && ! -e "$ROOT/nft-actions" ]]
  printf 'managed private core\n' > "$ROOT/etc/init.d/ctlvps-singbox-private"
  clear_actions
  reset_populations
  PURGE=0
  # Stopped/disabled proxy instances have no runlevel links. OpenRC returns
  # failure for deleting an already absent link; only enabled services need it.
  mkdir -p "$ROOT/etc/runlevels/default"
  ln -s /etc/init.d/ctlvps-agent "$ROOT/etc/runlevels/default/ctlvps-agent"
  ln -s /etc/init.d/ctlvps-proxy-guard "$ROOT/etc/runlevels/default/ctlvps-proxy-guard"
  plan > /dev/null
  execute_plan > /dev/null
  [[ ! -e "$ROOT/etc/init.d/ctlvps-agent" && ! -e "$ROOT/etc/init.d/ctlvps-singbox" && ! -e "$ROOT/etc/init.d/ctlvps-singbox-private" ]]
  [[ ! -e "$ROOT/etc/init.d/ctlvps-proxy-guard" && ! -e "$ROOT/etc/init.d/ctlvps-service-host" ]]
  [[ -f "$ROOT/etc/init.d/ctlvpsd" && -f "$ROOT/var/lib/ctlvps-agent/state.json" && -f "$ROOT/etc/ctlvps/agent-nat.json" ]]
  [[ "$(cat "$ROOT/cgroup-actions")" == $'openrc-stop ctlvps-agent.service\nopenrc-stop ctlvps-proxy-guard.service\nopenrc-stop ctlvps-singbox.service\nopenrc-stop ctlvps-singbox-private.service\nopenrc-stop ctlvps-service-host.service' ]]
  [[ "$(cat "$ROOT/runlevel-actions")" == $'delete ctlvps-agent default\ndelete ctlvps-proxy-guard default' ]]
  check_openrc_groups_empty
)
printf 'Uninstaller argument, NAT and OpenRC cleanup checks passed\n'
