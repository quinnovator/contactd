#!/usr/bin/env bash
# One local status-page watch, with both child processes owned by this script.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
data_dir="${CONTACTD_A2A_DEMO_DATA:-$root/data/a2a-demo}"
addr="127.0.0.1:${CONTACTD_A2A_DEMO_PORT:-8080}"
site_addr="127.0.0.1:${CONTACTD_A2A_SITE_PORT:-9090}"
url="http://$addr"
server_pid=
site_pid=
cleanup() {
  [[ -z "$server_pid" ]] || kill "$server_pid" 2>/dev/null || true
  [[ -z "$site_pid" ]] || kill "$site_pid" 2>/dev/null || true
  [[ -z "$server_pid" ]] || wait "$server_pid" 2>/dev/null || true
  [[ -z "$site_pid" ]] || wait "$site_pid" 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 130' INT TERM
mkdir -p "$data_dir"
if curl -sf "$url/healthz" >/dev/null || curl -sf "http://$site_addr" >/dev/null; then
  echo "A demo port is already in use; choose CONTACTD_A2A_DEMO_PORT and CONTACTD_A2A_SITE_PORT." >&2
  exit 1
fi
"$root/bin/demosite" -addr="$site_addr" >"$data_dir/site.log" 2>&1 &
site_pid=$!
"$root/bin/contactd-dev" -addr="$addr" -socket=off -data-dir="$data_dir" -infer=fake \
  -a2a-base-url="$url" -helpers=on -helpers-min-every=30s -fetch-allow="$site_addr" \
  -log-format=text >"$data_dir/server.log" 2>&1 &
server_pid=$!
for _ in $(seq 100); do
  if curl -sf "$url/healthz" >/dev/null && curl -sf "http://$site_addr" >/dev/null; then break; fi
  if ! kill -0 "$server_pid" 2>/dev/null || ! kill -0 "$site_pid" 2>/dev/null; then
    tail -n 5 "$data_dir/server.log" "$data_dir/site.log" >&2
    exit 1
  fi
  sleep 0.2
done
curl -sf "$url/healthz" >/dev/null
# Use a dedicated data directory so an existing Ada without helpers is not altered.
contacts=$(curl -fsS "$url/v1/contacts?name=ada")
id=$(python3 -c 'import json,sys; cs=json.load(sys.stdin)["contacts"]; print(cs[0]["id"] if cs else "")' <<<"$contacts")
if [[ -z "$id" ]]; then
  curl -fsS -X POST "$url/v1/contacts" -H 'Content-Type: application/json' \
    -d '{"name":"ada","agent":{"system":"You are Ada, a thoughtful colleague. Use standing helpers to watch pages. Report changes and stay quiet for routine updates.","helpers":true}}' >/dev/null
fi
cat <<RUNBOOK
A2A helper demo. Logs: $data_dir/server.log and $data_dir/site.log

1. Ask Ada: "Watch http://$site_addr every 30 seconds for the next half hour.
   Report a baseline once, then tell me only when a component changes."
2. Open /a2a, select an exchange, press enter, then escape.
3. Wait for a quiet check in the helper strip.
4. In another terminal:
   curl -XPOST 'http://$site_addr/set?component=API&state=degraded'
5. Wait for the report, then open /helper 1. Escape returns to Ada.
6. Ask: "Thanks, stop watching now."
7. Reset with: curl -XPOST 'http://$site_addr/reset'

The server and site stop when chat exits. A model provider key is required.
RUNBOOK
if [[ "${CONTACTD_A2A_DEMO_NO_CHAT:-}" == 1 ]]; then
  wait "$server_pid"
else
  "$root/bin/contactd-dev" chat -url="$url" -config-dir="$data_dir/chat-config" "$@" ada
fi
