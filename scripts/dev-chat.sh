#!/usr/bin/env bash
# Keep the chat attached to this terminal and to the dev server's lifetime.
# Wait for the API before opening the picker, which loads contacts immediately.
set -uo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
lifetime="$1"
shift
chat_pid=
cleanup() {
  if [[ -n "$chat_pid" ]] && kill -0 "$chat_pid" 2>/dev/null; then
    kill -TERM "$chat_pid" 2>/dev/null || true
    wait "$chat_pid" || true
  fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

while [[ -e "$lifetime" ]]; do
  if "$root/bin/contactd-dev" chat -list "$@" >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done
[[ -e "$lifetime" ]] || exit 0

# An asynchronous command otherwise inherits /dev/null as stdin in Bash.
"$root/bin/contactd-dev" chat "$@" <&0 &
chat_pid=$!
while [[ -e "$lifetime" ]] && kill -0 "$chat_pid" 2>/dev/null; do
  sleep 0.2
done
if kill -0 "$chat_pid" 2>/dev/null; then
  kill -TERM "$chat_pid" 2>/dev/null || true
fi
wait "$chat_pid"
