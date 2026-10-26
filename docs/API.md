# API

contactd serves one HTTP API, on a TCP address and on a Unix socket. There are no thread endpoints; that's the point. A client addresses a contact, and the server decides each turn what history matters.

## Endpoints

| Method | Path | Purpose |
| --- | --- | --- |
| POST | `/v1/contacts` | Create a contact from an agent config (below), an optional `name` and an optional `profile`. An unknown provider, model or effort is a 400 here, and a name another contact has is a 409 |
| GET | `/v1/contacts` | The contacts a person talks to, most recently active first; sub-agents are left out. `?name=` finds one by its exact name |
| GET | `/v1/contacts/{id}/helpers` | Durable helper snapshots as `{"helpers": [...]}`; empty when none, 404 for an unknown contact. No credentials. |
| GET | `/v1/contacts/{id}` | One contact: name, agent config, profile, and the route that serves it with the deployment's defaults filled in |
| PATCH | `/v1/contacts/{id}` | `{"name": ...}` renames; an empty name removes it |
| POST | `/v1/contacts/{id}/messages` | Send `{"text": ...}` with optional `session_id`, `time_zone` and `presentation`; returns 202 with the durable `seq` |
| POST | `/v1/contacts/{id}/interrupt` | Stop the reply in flight; 202 |
| GET | `/v1/contacts/{id}/stream` | SSE: `ready`, `user`, `typing`, `text`, `tool_start`, `tool_call`, `tool_result`, `message`, `interrupted`, `notice`, `helper`, `error` |
| GET | `/v1/contacts/{id}/live` | WebSocket: send an optional `hello` (see below), then `{"type":"message","text":...}`, `{"type":"typing"}` and `{"type":"interrupt"}`; receive the same deltas, and for each message one `ack` or one `error` frame, in the order sent |
| GET | `/v1/contacts/{id}/events` | Page through the log in log order: `?after={seq}` forward, `?before={seq}` backward, `?tail=1` backward from the end, each with `limit`. `?kinds=user_msg,agent_msg` keeps only those kinds; the others are `tool_call`, `tool_result`, `attempt`, `checkpoint`, `notice`, `summary` and `env_snapshot` |
| GET | `/v1/contacts/{id}/files/{path...}` | One file from the contact's tier-0 working directory, for a client that shows what a tool wrote. At most 8 MiB (413 beyond). PNG, JPEG, GIF, WebP and BMP are served as themselves, by their first bytes; anything else as `application/octet-stream` with `Content-Disposition: attachment`. A file is sent with an `ETag`, and `If-None-Match` gives 304. There is no listing, and a path that leaves the directory, by name or through a symbolic link, is a 404 like a file that is not there |
| GET | `/metrics`, `/healthz` | Prometheus metrics; liveness (503 while draining) |

Quick check against a running server:

```sh
curl -s -XPOST localhost:8080/v1/contacts \
  -d '{"agent":{"system":"You are Ada, a thoughtful colleague."},"profile":"Prefers short answers."}'
# {"id":1}

curl -sN localhost:8080/v1/contacts/1/stream &
curl -s -XPOST localhost:8080/v1/contacts/1/messages -d '{"text":"hey, are you around?"}'
# {"seq":2}   <- 202 once the message is durable; seq 1 is the visit's session-start notice
```

## Access

The same API is served on a Unix socket, `contactd.sock` in the data directory, with permissions `0600`. Nothing else about the API is authenticated: the TCP address is for a trusted network, and the socket is for the user who runs the server. Put a reverse proxy with authentication in front of the TCP address before exposing it anywhere else.

`-socket=off` disables the socket. A data directory whose path is too long for a socket address is logged and skipped unless `-socket` was given by name.

## Sending messages

A message is acknowledged with 202 and its `seq` once it is durable in the log. The contact waits for a burst of messages to end before it replies (700 ms after the last message or typing indicator, at most 3 s after the first), and a message sent while it is replying interrupts the reply.

Unanswered messages are capped per contact at 128 KiB of text. A message beyond that is refused with 429 (or an `error` frame on the WebSocket), so a client that sends faster than the model answers cannot grow the prompt without limit. A refused message does not interrupt the turn in flight, so the backlog clears. A refusal on the WebSocket never closes it; only shutdown ends the connection.

## Reading a contact without missing anything

A stream runs ahead of the log's group commit, and the log is the only place the past is. A client that wants both opens the stream first. The first delta is always `ready`, and its `seq` is the last sequence number assigned before the stream began (zero when the contact was not in memory). Everything up to there is read from `/events`; everything after it arrives on the stream. Deltas that announce an event carry its `seq`, so reading an event both ways is harmless. A stream opened mid-reply is then sent `typing` and one `text` delta with what has been said so far.

`ready` is advisory about durability: the event it names may be a moment from readable. `internal/client` waits for it for at most two seconds, and carries on without it if the write failed.

Clients should ignore delta types they do not know.

## Visits and local time

A client may introduce a visit with `{"type":"hello","session_id":"a-client-generated-id","time_zone":"America/Chicago","presentation":"avatar"}` on the WebSocket, or the same fields on `POST /messages`. `presentation` is `avatar` or `text`; `time_zone` is an IANA name. Invalid metadata is rejected. The terminal client mints an ID for each open contact and resends it on reconnect. Opening and closing without speaking leaves no log entry and triggers no greeting.

The first accepted message of a visit logs a `session-start` notice immediately before the message. It records the person's local date and time, the gap since the last exchange, and the presentation mode. A first meeting is described as such. These notices are model memory, hidden in the terminal client and excluded from recall queries. Reusing an ID does not create another notice, including after hibernation or a restart. Clients without an ID start a new visit after 30 minutes without an exchange. The last supplied zone persists; until one is supplied, time is UTC. The binary embeds time-zone data, including daylight-saving rules.

## Contacts and agent configs

A contact's **name** is what a person calls it. It is optional, unique where given, and never a number. It is stored beside the agent config, not in it, so naming a contact changes no byte of its prompt.

Every contact's own prompt follows a fixed server preamble: the character treats prior events as memories and runtime notices as surroundings. Profile, recall and summaries use that perspective. The preamble also applies to sub-agents, whose task instructions still describe their assignment.

An agent config:

| Field | Meaning |
| --- | --- |
| `system` | The contact's own system prompt, after the fixed server preamble |
| `provider`, `model` | The route that serves this contact. Either may be omitted to take the deployment's default. Names resolve strictly: a model the provider does not serve is an error, never a substitute |
| `effort` | Reasoning effort on one scale for every provider: `none`, `low`, `medium`, `high`, `xhigh`, `max`. The provider sends the nearest value the model accepts, and the log records what was sent |
| `max_tokens` | Output cap per model call. With none, a call asks for the route's default (8,192 tokens on most routes, 64,000 on Anthropic), not its maximum |
| `tools` | Code-execution tools (below) |
| `delegate` | `true` gives the agent the built-in `delegate` tool, which starts sub-agents |

A profile cannot be changed after the contact is created yet.

### Tools

A tool is a code-execution tool: it takes one `code` argument and names the environment it runs in.

```json
{"name": "python", "description": "Run Python 3.12. Files persist; variables may not.",
 "env": {"tier": 0, "base": "python-3.12"}}
```

Tier 0 runs in-process under wazero (`python-3.12`, `quickjs`); files persist in the contact's working directory, and interpreter variables persist only within a lease. Tier 1 is a thin client to an external snapshot-sandbox service (`-sandbox-url`). Arbitrary function tools are not modelled; the exceptions are `delegate` and, on native DeepSeek routes, `web_search` and `web_fetch` (see [PROVIDERS.md](PROVIDERS.md#deepseek-web-search-and-fetch)).

### Sub-agents

With `delegate` on, the model can call `delegate({"task": ..., "mode": "spawn" | "fork"})`. The call returns at once with the sub-agent's ID; the model cannot wait for it or poll it. A sub-agent is a contact of its own, with its own log (readable at `/v1/contacts/{child}/events`) and its parent's instructions, tools and route.

- **`spawn`** starts it from the instructions and the task.
- **`fork`** also gives it the parent's conversation up to the parent's last finished reply. Its first request begins with the exact bytes the parent last sent, so it reads the parent's prompt cache.
- **Settlement** arrives in the parent's log as a `notice` event, and starts a turn so the agent can act on it. It never interrupts a reply in progress, and a sub-agent that fails settles the same way: as a notice, with a diagnostic of at most 4,096 bytes. A sub-agent's result is its last message.
- **A settlement survives a restart.** The parent's log records each sub-agent it started and each one it has heard from. A parent that wakes with one still open asks it to report again from its own log; a sub-agent that was cut off mid-task reports that it failed.
- **Limits:** one level deep, and `-max-children` (8) running at once per contact. A sub-agent is shown the `delegate` tool and refused when it calls it, so its tool list, and with it the first bytes of its prompt, match its parent's. Interrupting the parent does not stop its sub-agents, and an interrupt sent to a sub-agent is ignored: it has a task to settle.

## Diagnostics endpoints

With `-diagnostics` (or `CONTACTD_DIAGNOSTICS=true`), the server also serves `/debug/plans`, which `ctxtop` reads. It replays up to 500 calls and streams planning, bounded progress updates and completion. `/debug/plans/{run}/{call}` serves details on demand; its `/events` endpoint pages through source events up to the log head captured for that call. Captures are in memory only, limited to 2 MiB per call and 32 MiB overall, with explicit truncation and eviction. Slow stream readers are disconnected and can replay retained state.

**Diagnostic mode exposes prompt previews, full prompt and tool contents and source log messages through the same socket and network access as the API.** Use it only where that access is appropriate. It does not capture authentication headers.

## Helpers over A2A

Set `agent.helpers: true` at contact creation to offer requester tools. `agent.helper` is runtime-only and is refused by this API. Helper contacts, like sub-agents, are omitted from the normal contact list. `GET /v1/contacts/{id}/helpers` returns handle, local contact (if any), title, task/context IDs, state/status message, schedule, counters, outcome, timestamps, and agent URL.

`notice` deltas carry `source`, the notice's origin (`helper-update`, `helper-check`, `subagent-settled` and so on), so a live view can show or hide a notice as it does the same notice read from `/events`; the terminal client hides `helper-check` notices except in `/helper N`. Live `helper` deltas carry a `helper` object with the handle, contact, title, task ID, state, event, method, direction, summary, redacted wire JSON, and progress. They are ephemeral diagnostics; `notice` deltas carrying `helper.handle` correspond to durable helper-update notices. The public card is at `/.well-known/agent-card.json`; protocol bindings and callbacks are under `/a2a/`. All A2A routes disappear with `-helpers=off`. See [A2A](A2A.md) for authentication, extensions and examples.
