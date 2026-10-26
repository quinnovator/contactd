# Standing helpers over A2A

A contact with `agent.helpers: true` can start background helpers, send them answers, stop them, and list them. Each helper owns one contact and one A2A task. It reads a brief, checks on a schedule, reports meaningful changes, and stays quiet otherwise. Reports become durable `helper-update` notices in the requesting contact's log. A notice waits behind an ongoing reply; it never interrupts it.

The requester uses real HTTP, including when both sides run in the same process. The helper agent implements A2A 1.0 with `a2a-go/v2 v2.7.0`, pinned against the v1.0.1 protocol definitions. JSON-RPC and HTTP+JSON share a backend. No gRPC binding is compiled. Helpers cannot delegate or start other helpers.

## Discovery and endpoints

| Endpoint | Purpose |
| --- | --- |
| `GET /.well-known/agent-card.json` | Public agent card, `Cache-Control: public, max-age=300`, ETag and 304 support |
| `POST /a2a/helpers` | JSON-RPC 2.0 binding |
| `/a2a/helpers/rest/` | HTTP+JSON binding |
| `POST /a2a/push` | Requester callback, authenticated per task |
| `GET /v1/contacts/{id}/helpers` | Credential-free requester snapshots |

The card advertises streaming, push notifications, both interfaces at protocol version `1.0`, and the optional schedule extension. SDK serialization omits `required: false`; its default is false. Inputs are text and JSON data parts; file and URL parts are rejected. `fetch_url` is a helper tool, separate from protocol URL parts.

Both bindings support SendMessage, SendStreamingMessage, GetTask, ListTasks, CancelTask, SubscribeToTask, and push-configuration CRUD. GetExtendedAgentCard returns the standard not-configured error. `A2A-Version: 1.0` is required: the SDK interprets an absent version as 0.3 and returns version-not-supported. SDK error mapping and ProtoJSON enum names are used throughout.

## Authentication

Bearer authentication is enabled by default. Startup uses `-a2a-token`, or creates `<data-dir>/a2a.token` with 32 random bytes encoded as base64url, mode 0600. Existing token files must be private. The public card declares the bearer security scheme. Protocol requests without a matching bearer receive HTTP 401 and `WWW-Authenticate`. Use `-a2a-auth=off` only for a local interoperability check.

The requester discovers and caches the card for five minutes. It uses the SDK AuthInterceptor with the `contactd` session and `bearer` credential. Calls have a 15-second timeout and announce the extension with `A2A-Extensions`. `-helper-agent-url` and `-helper-agent-token` can select another contactd server. Configure that server's push allowlist to include this requester's callback address. Remote helpers do not expose a local contact ID.

## Helper extension v1

URI: `https://github.com/quinnovator/contactd/blob/main/docs/A2A.md#helper-extension-v1`

Place an object under that URI in **message.metadata**, not request-level metadata:

```json
{
  "title": "Status page watch",
  "every": "1m",
  "until": "2026-10-27T15:00:00.000Z",
  "maxChecks": 61
}
```

`title` is 1–60 characters without controls. `every` is a Go duration between the configured minimum and 24 hours. `until` is a future UTC timestamp within 168 hours. `maxChecks` is 1–500; absent, it is the smaller of 500 and `ceil(duration/every)+1`. The initial brief counts as check 1. Without extension metadata the defaults are every 15 minutes for 24 hours, and a title derived from the brief.

Task metadata under the same URI includes `title`, `every`, `until`, `maxChecks`, `checks`, and `helperContact`; when applicable it also includes `nextCheck`, `lastCheck`, and `lastOutcome`. Outcomes are `reported`, `quiet`, `asked`, `finished`, and `failed`. Artifact metadata includes `trigger` (`brief`, `check`, or `message`), `check`, and the durable log `seq`.

## Task lifecycle

A new message creates a server-generated task and context, unless it supplies a context ID. A client task ID must already exist, and its context must agree. Global capacity exhaustion creates a `TASK_STATE_REJECTED` task without a helper contact.

Tasks move from SUBMITTED to WORKING after the brief is durable. `ask_requester` moves to INPUT_REQUIRED and pauses checks; a subsequent message resumes WORKING. `finish`, elapsed duration, and the check limit complete the task. Three consecutive failed turns fail it. A successful outcome resets the failure count. CancelTask moves to CANCELED. Waiting for an answer does not extend the expiry time.

Terminal tasks are immutable. Sending to one is unsupported; canceling one is not-cancelable. Cancellation allows an already-running model turn to finish but suppresses its later reports. A durable terminal log-sequence boundary, added in migration 0005, also suppresses reports written in the same millisecond as cancellation. A busy helper skips that scheduled check and advances its deadline; downtime causes one overdue check rather than replaying every missed interval.

Requester tools are `start_helper`, `message_helper`, `stop_helper`, `list_helpers`, and `stay_quiet`. Helpers receive `fetch_url`, `ask_requester`, `finish`, and `stay_quiet`, plus optional Python. Even a configuration with both role flags remains helper-only. The HTTP API rejects attempts to set the runtime `agent.helper` flag.

## History, artifacts and streams

User messages are history, preserving the original brief rather than its runtime wrapper. Helper replies are complete text artifacts, with IDs `report-<log-seq>`, `lastChunk: true`, and no append flag. Interrupted replies are excluded. Snapshots contain at most 100 history messages and 50 artifacts; `historyLength: 0` omits history. The outbox scans the full log, so this snapshot bound does not drop pending reports.

Blocking SendMessage waits for a terminal or interrupted state; `configuration.returnImmediately: true` returns promptly. SubscribeToTask and SendStreamingMessage send a Task snapshot first, followed by ordered updates. Streams close on terminal, INPUT_REQUIRED, or AUTH_REQUIRED. SubscribeToTask rejects terminal tasks. Slow subscribers are disconnected after their bounded queue fills. Multiple subscribers receive the same events.

ListTasks supports context, state, inclusive `statusTimestampAfter` (`>=`, matching the pinned proto), and cursor pagination. The maximum page is 100. List and Get refreshes do not consume the requester's status-notification cursor.

## Push delivery and webhook

Push URLs must be HTTP(S), without userinfo, and match an explicitly allowed host:port. The sender pins DNS resolutions during validation, rechecks at dial time, disables proxy environment settings, and refuses redirects. Authentication schemes are empty, Bearer, or Basic. Configurations can be supplied in SendMessage or managed through the protocol CRUD methods; delete is idempotent.

Each push body is the SDK StreamResponse envelope. Headers are `Content-Type: application/a2a+json`, `A2A-Version: 1.0`, optional `A2A-Notification-Token`, and configured Authorization. Every config is attempted. A report cursor advances only after all configs succeed. Failures retry from one second with exponential backoff capped at five minutes, abandoning a continuously failing delivery after 24 hours of process uptime. Restart resets that in-memory retry budget. Quiet-check heartbeats are best effort and do not change the durable state cursor.

The callback accepts `application/a2a+json` or `application/json`, limits bodies to 1 MiB, looks up the notification token (or task ID), and checks the bearer secret in constant time. Unknown/mismatched tasks return 404, bad auth 401, invalid content 400/415, and failed durable acknowledgement 503. A first push can adopt the task ID before SendMessage returns.

Artifact IDs are deduplicated in SQLite. Notices wait up to ten seconds for the actor's durable acknowledgement before the callback accepts them. Questions and stopped states notify separately from state refreshes. A cancellation requested through `stop_helper` does not create a redundant notice. WORKING heartbeats never wake the requester.

Wire diagnostics recursively redact `token` and `credentials` before UTF-8-safe clipping to 2 KiB. Snapshots omit secrets; wire deltas are ephemeral and never hydrate or wake a contact.

## Restarts and limits

Schedules, tasks, push configurations, report cursors, remote helper rows, accepted artifact IDs, and conversation logs survive restarts. Resume with the same base URL so existing callback URLs remain reachable. Active helper restart and callback replay are covered by the repeated race-detector loop tests.

There are remaining crash windows:

- If a requester notice commits before its follow-up turn starts and the process dies, the next message or notice triggers the response.
- Notice insertion, artifact deduplication, and counter updates are separate commits. A crash between them can duplicate a notice or leave its counter behind. Delivery is at least once, not transactional exactly once.
- An abrupt exit during initial task creation can leave a SUBMITTED row without a runnable brief. Inspect and cancel such a task.

Failed starts retain a failed requester row rather than deleting it, so helper handles and callback identities cannot be reused. A timeout that already received a callback retains the adopted task. These choices and the terminal sequence boundary are intentional refinements of the design document.

## Configuration

All flags also accept `CONTACTD_` plus the uppercase, underscore-separated name.

| Flag | Default |
| --- | --- |
| `-helpers` | `on`; `off` removes all A2A routes and requester tools |
| `-a2a-base-url` | HTTP URL derived from `-addr`; wildcard bind hosts become loopback |
| `-a2a-auth` | `token` |
| `-a2a-token` | private token file in the data directory |
| `-a2a-push-allow` | base URL host:port |
| `-helper-agent-url`, `-helper-agent-token` | local agent URL and token |
| `-helper-provider`, `-helper-model`, `-helper-effort` | deployment defaults |
| `-helper-tools` | none; `python` adds the tier-0 Python tool |
| `-helpers-min-every` | `1m` |
| `-helpers-max-active` | `16` |
| `-helpers-per-contact` | `4` |
| `-helpers-concurrency` | `4` simultaneous check dispatches |
| `-fetch-allow` | no private-address exceptions |

Fetches time out after 15 seconds, follow at most five redirects, and read at most 2 MiB. Loopback, private, link-local, unspecified, and multicast addresses are refused at dial time unless explicitly allowed. HTML is reduced to text; scripts and styles are discarded. These limits apply to every redirect.

Credentials are stored in plaintext in the private data directory, like the existing affinity key. Fetched pages are untrusted data; the helper prompt says so, and helpers have no world-changing tools by default. This reduces prompt-injection risk but does not eliminate it. All server text is sanitized before terminal display.

## Metrics

`contactd_helper_tasks` counts active tasks. `contactd_helper_checks_total{outcome}` counts started, busy, and error dispatches. `contactd_helper_turns_total{outcome}` counts reported, quiet, asked, finished, and failed turns. `contactd_a2a_requests_total{method,result}`, `contactd_a2a_push_total{kind,result}`, and `contactd_a2a_webhook_total{result}` describe protocol handling and callback delivery. Labels contain no task IDs, message text, or credentials.

## Examples

```sh
curl -s http://localhost:8080/.well-known/agent-card.json
TOKEN=$(cat data/a2a.token)
curl -s http://localhost:8080/a2a/helpers \
  -H 'Content-Type: application/json' -H 'A2A-Version: 1.0' \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{
    "message":{"messageId":"example-1","role":"ROLE_USER","parts":[{"text":"Read https://example.com and report meaningful changes."}]},
    "configuration":{"returnImmediately":true}}}'
curl -s http://localhost:8080/v1/contacts/1/helpers
```

Create a requester with `POST /v1/contacts` and `{"name":"ada","agent":{"helpers":true,"system":"You are Ada, a thoughtful colleague."}}`. In chat, ask it to watch a URL. Use `/a2a` for live traffic, `/helpers` for the strip, and `/helper N` for a local helper's read-only log.

## Conformance

SDK-client tests exercise both transports, standard errors, card serialization, streaming, push CRUD and wire fixtures. Race-detector tests cover the complete HTTP loop, changing pages, questions, cancellation, and active restart. A real DeepSeek-backed watch was verified to send a baseline, stay quiet on subsequent checks, and cancel on request.

The separate `a2a:tck` runner and TCK results are documented here when conformance validation completes. Scripted TCK behavior belongs only in the SUT command, never in the production helper service.
