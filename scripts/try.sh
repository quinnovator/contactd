#!/usr/bin/env bash
# Start a server in the background and open a chat with Ada in this terminal.
# The server stops when the chat closes. Arguments are passed to the chat.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
bin="$root/bin/contactd-dev"
data_dir="${CONTACTD_DATA_DIR:-$root/data}"
addr="127.0.0.1:${CONTACTD_TRY_PORT:-8080}"
url="http://$addr"
name=ada
system="You are Ada, a warm, curious colleague. Keep replies short and conversational. When code helps, show it in a fenced block."

# With no provider configured, the scripted model answers instead.
llm=()
if [[ -z "${CONTACTD_LLM:-}${DEEPSEEK_API_KEY:-}${ANTHROPIC_API_KEY:-}${OPENROUTER_API_KEY:-}${OPENCODE_API_KEY:-}${OLLAMA_API_KEY:-}${OPENAI_API_KEY:-}${CONTACTD_OPENAI_BASE_URLS:-}" ]]; then
  llm=(-llm=fake)
  echo "No model provider key found, so Ada's replies are scripted."
  echo "Set one (DEEPSEEK_API_KEY, ANTHROPIC_API_KEY, ...) and run this again to talk to a real model."
  echo
fi
wasm_dir=
[[ ! -d third_party/wasm ]] || wasm_dir=./third_party/wasm

server_pid=
stop_server() {
  [[ -z "$server_pid" ]] || kill "$server_pid" 2>/dev/null || true
  [[ -z "$server_pid" ]] || wait "$server_pid" 2>/dev/null || true
}
trap stop_server EXIT

# A server already answering on the address is used as it is.
if ! curl -sf "$url/healthz" >/dev/null 2>&1; then
  mkdir -p "$data_dir"
  log="$data_dir/try.log"
  "$bin" ${llm[@]+"${llm[@]}"} -data-dir="$data_dir" -addr="$addr" -infer=fake \
    -wasm-dir="$wasm_dir" -log-format=text >"$log" 2>&1 &
  server_pid=$!
  echo "Starting contactd on $addr (log: ${log#"$root"/})..."
  for _ in $(seq 100); do
    curl -sf "$url/healthz" >/dev/null 2>&1 && break
    if ! kill -0 "$server_pid" 2>/dev/null; then
      server_pid=
      echo "contactd did not start:" >&2
      tail -n 5 "$log" >&2
      exit 1
    fi
    sleep 0.2
  done
fi

chat=("$bin" chat -url="$url" "$@")
if "${chat[@]}" -list | cut -f2 | grep -qx "$name"; then
  "${chat[@]}" "$name"
else
  "${chat[@]}" -new "$name" -system "$system"
fi
