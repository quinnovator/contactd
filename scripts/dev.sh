#!/usr/bin/env bash
# Run the server here, with chat and diagnostics in separate terminal windows.
# Both clients use the same connection overrides and server lifetime.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
data_dir="${CONTACTD_DATA_DIR:-$root/data}"
socket="${CONTACTD_SOCKET:-}"
addr=127.0.0.1:8080
diagnostics=true
args=("$@")
for ((i=0; i<${#args[@]}; i++)); do
  arg="${args[i]}"
  [[ "$arg" != --* ]] || arg="${arg#-}"
  case "$arg" in
    -data-dir=*) data_dir="${arg#*=}" ;;
    -data-dir) i=$((i+1)); data_dir="${args[i]:?missing -data-dir value}" ;;
    -socket=*) socket="${arg#*=}" ;;
    -socket) i=$((i+1)); socket="${args[i]:?missing -socket value}" ;;
    -addr=*) addr="${arg#*=}" ;;
    -addr) i=$((i+1)); addr="${args[i]:?missing -addr value}" ;;
    -diagnostics=*)
      case "${arg#*=}" in 0|f|F|false|False|FALSE) diagnostics=false ;; *) diagnostics=true ;; esac
      ;;
    -diagnostics) diagnostics=true ;;
    -h|-help|--help) exec "$root/bin/contactd-dev" -h ;;
  esac
done

# Let the server honor CONTACTD_LLM / CONTACTD_MODEL and configured providers.
# Only local recall uses the lightweight backend; scripted replies require an
# explicit -llm=fake.
defaults=("-data-dir=${CONTACTD_DATA_DIR:-$root/data}" -addr=127.0.0.1:8080
  -infer=fake -wasm-dir=./third_party/wasm -log-format=text -diagnostics)
lifetime="$(mktemp "${TMPDIR:-/tmp}/contactd-dev.XXXXXX")"
server_pid=
window_backend=
window_ids=()
close_windows() {
  local window_id
  if (( ${#window_ids[@]} == 0 )); then return; fi
  for window_id in "${window_ids[@]}"; do
    case "$window_backend" in
      ghostty)
        osascript - "$window_id" >/dev/null <<'APPLESCRIPT' || true
on run argv
  tell application "Ghostty"
    if exists (window id (item 1 of argv)) then
      close window (window id (item 1 of argv))
    end if
  end tell
end run
APPLESCRIPT
        ;;
      terminal)
        osascript - "$window_id" >/dev/null <<'APPLESCRIPT' || true
on run argv
  tell application "Terminal"
    if exists (window id ((item 1 of argv) as integer)) then
      close (window id ((item 1 of argv) as integer))
    end if
  end tell
end run
APPLESCRIPT
        ;;
      tmux) tmux kill-window -t "$window_id" 2>/dev/null || true ;;
    esac
  done
}
cleanup() {
  if [[ -n "$server_pid" ]] && kill -0 "$server_pid" 2>/dev/null; then
    kill -TERM "$server_pid" 2>/dev/null || true
    wait "$server_pid" || true
  fi
  rm -- "$lifetime"
  close_windows
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

"$root/bin/contactd-dev" "${defaults[@]}" "$@" &
server_pid=$!

connection_args=()
if [[ "$socket" == off ]]; then
  # Wildcard listen addresses are not client destinations.
  client_addr="$addr"
  case "$client_addr" in
    :*) client_addr="127.0.0.1$client_addr" ;;
    0.0.0.0:*) client_addr="127.0.0.1:${client_addr##*:}" ;;
    \[::\]:*) client_addr="[::1]:${client_addr##*:}" ;;
  esac
  connection_args+=("-url=http://$client_addr")
else
  if [[ -z "$socket" ]]; then socket="$data_dir/contactd.sock"; fi
  [[ "$socket" == /* ]] || socket="$root/$socket"
  connection_args+=("-url=" "-socket=$socket")
fi
open_window() {
  local title="$1"
  shift
  local window_cmd window_id
  printf -v window_cmd '%q ' "$@"
  if [[ "$(uname -s)" == Darwin ]]; then
    if [[ "${TERM_PROGRAM:-}" == ghostty || "${CONTACTD_DEV_VIEWER:-}" == ghostty ]]; then
      window_backend=ghostty
      window_id="$(osascript - "$root" "$window_cmd" <<'APPLESCRIPT'
on run argv
  tell application "Ghostty"
    set spawnedWindow to new window with configuration {initial working directory:item 1 of argv, command:item 2 of argv, wait after command:false}
    return id of spawnedWindow
  end tell
end run
APPLESCRIPT
      )" || return 1
      window_ids+=("$window_id")
    else
      window_backend=terminal
      window_id="$(osascript - "$window_cmd" <<'APPLESCRIPT'
on run argv
  tell application "Terminal"
    set spawnedTab to do script item 1 of argv
    set spawnedTTY to tty of spawnedTab
    activate
    repeat with candidate in windows
      repeat with candidateTab in tabs of candidate
        if tty of candidateTab is spawnedTTY then return id of candidate
      end repeat
    end repeat
    error "Could not identify the new Terminal window"
  end tell
end run
APPLESCRIPT
      )" || return 1
      window_ids+=("$window_id")
    fi
  elif [[ -n "${TMUX:-}" ]]; then
    window_backend=tmux
    window_id="$(tmux new-window -P -F '#{window_id}' -n "$title" "$window_cmd")" || return 1
    window_ids+=("$window_id")
  elif command -v ghostty >/dev/null 2>&1; then
    ghostty +new-window --working-directory="$root" --title="$title" -e "$@"
  elif command -v x-terminal-emulator >/dev/null 2>&1; then
    x-terminal-emulator -T "$title" -e "$@"
  else
    return 1
  fi
}

if [[ "${CONTACTD_DEV_VIEWER:-auto}" != off ]]; then
  if [[ "$socket" == off && "$addr" == *:0 ]]; then
    echo 'dev: choose a fixed -addr with -socket=off to open the chat and diagnostics windows' >&2
  else
    if ! open_window chat bash "$root/scripts/dev-chat.sh" "$lifetime" \
      "-data-dir=$data_dir" "-config-dir=${CONTACTD_CONFIG_DIR:-$data_dir/chat-config}" "${connection_args[@]}"; then
      echo 'dev: could not open chat; run mise run chat in another terminal' >&2
    fi
    if [[ "$diagnostics" != false ]]; then
      if ! open_window ctxtop "$root/bin/ctxtop" "-lifetime-file=$lifetime" "${connection_args[@]}"; then
        echo 'dev: could not open diagnostics; run mise run ctxtop in another terminal' >&2
      fi
    fi
  fi
fi
wait "$server_pid"
