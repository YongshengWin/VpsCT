#!/usr/bin/env bash
# Retry transport errors only. Go's module checksum and TLS checks stay enabled.
set -euo pipefail
log=$(mktemp)
trap 'rm -f -- "$log"' EXIT
for attempt in 1 2 3; do
 printf 'Module download attempt %s/3\n' "$attempt"
 if [[ "$attempt" == 1 ]]; then
  if go mod download all >"$log" 2>&1; then cat "$log"; exit 0; fi
 else
  # Avoid the proxy HTTP/2 stream failure seen on hosted runners; retain TLS.
  if GODEBUG="${GODEBUG:+$GODEBUG,}http2client=0" go mod download all >"$log" 2>&1; then cat "$log"; exit 0; fi
 fi
 cat "$log" >&2
 if grep -Eqi 'checksum mismatch|SECURITY ERROR|verifying module|certificate|unknown revision|invalid version' "$log"; then exit 1; fi
 if ! grep -Eqi 'INTERNAL_ERROR|unexpected EOF|connection reset|TLS handshake timeout|i/o timeout|502 Bad Gateway|503 Service Unavailable|504 Gateway Timeout|temporary failure|no such host' "$log"; then exit 1; fi
 [[ "$attempt" != 3 ]] || exit 1
 printf 'Transient download failure; retrying in %s seconds\n' "$attempt"
 sleep "$attempt"
done
