#!/usr/bin/env bash
# Optional interoperability check. Keeps the upstream report and its exit code,
# including known exceptions documented in docs/A2A.md.
set -euo pipefail
cd "$(dirname "$0")/.."
work=${CONTACTD_A2A_TCK_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/contactd-a2a-tck.XXXXXX")}
revision=5daf0144b4a4adbdefb62180739b13c5bd7d8196 # 1.0.0.alpha3
mkdir -p "$work"
if [[ ! -d "$work/tck/.git" ]]; then
  git clone --depth 1 --branch 1.0.0.alpha3 https://github.com/a2aproject/a2a-tck.git "$work/tck"
fi
if [[ $(git -C "$work/tck" rev-parse HEAD) != "$revision" ]]; then
  echo "TCK checkout is not the pinned revision: $work/tck" >&2
  exit 1
fi
if curl -fsS --max-time 1 http://127.0.0.1:9999/.well-known/agent-card.json >/dev/null 2>&1; then
  echo 'Port 9999 is already serving an agent; stop it before running the isolated SUT.' >&2
  exit 1
fi
go build -o "$work/a2a-sut" ./cmd/a2a-sut
"$work/a2a-sut" -addr=127.0.0.1:9999 >"$work/sut.log" 2>&1 &
sut_pid=$!
cleanup() {
  kill "$sut_pid" 2>/dev/null || true
  wait "$sut_pid" 2>/dev/null || true
  echo "TCK HTML report: $work/tck/reports/tck_report.html"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
ready=0
for ((i=0; i<100; i++)); do
  kill -0 "$sut_pid" 2>/dev/null || { cat "$work/sut.log" >&2; exit 1; }
  if curl -fsS http://127.0.0.1:9999/.well-known/agent-card.json >/dev/null; then ready=1; break; fi
  sleep .1
done
[[ "$ready" == 1 ]] || { echo 'SUT did not start' >&2; exit 1; }
cd "$work/tck"
uv sync --frozen
uv run --frozen run_tck.py --sut-host http://127.0.0.1:9999 --transport jsonrpc,http_json --level must "$@"
