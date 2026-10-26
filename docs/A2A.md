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
- An abrupt exit during initial task creation can leave a SUBMITTED row without a confirmed runnable brief. Startup marks it FAILED with an explanation; start a new helper. The brief is never replayed blindly.

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

## Demo

With one supported model provider key in the environment, run `mise run demo:a2a`. The launcher uses a separate `data/a2a-demo` directory, creates Ada with helpers enabled, and opens chat. It stops the site and server on exit. Server and site logs are in that directory. `CONTACTD_A2A_DEMO_DATA`, `CONTACTD_A2A_DEMO_PORT` (8080), and `CONTACTD_A2A_SITE_PORT` (9090) override the demo locations.

Ask Ada to watch `http://127.0.0.1:9090` every 30 seconds for half an hour. After the baseline and a quiet check, run `curl -XPOST 'localhost:9090/set?component=API&state=degraded'` in another terminal. The next check should report the change. Inspect `/a2a` and `/helper 1`, then ask Ada to stop. `curl -XPOST localhost:9090/reset` restores all four components. States are operational, degraded, outage, and maintenance.

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

Run `mise run a2a:tck` for the optional upstream MUST suite against both bindings. It builds `cmd/a2a-sut`, starts it on loopback port 9999, installs the TCK with uv, and saves its HTML/JUnit reports in a temporary directory printed on exit. `CONTACTD_A2A_TCK_DIR` selects a reusable directory. Python 3.11+ and network access are required; this task is deliberately outside CI. The TCK is pinned to `1.0.0.alpha3`, commit `5daf0144b4a4adbdefb62180739b13c5bd7d8196`, with its frozen dependency lock. The runner preserves upstream failures and exit status; it does not suppress or mark exceptions as passes.

The SUT uses the production card, version/capability enforcement, HTTP handlers and SDK serializers with a separate in-memory backend. Message-ID scripts exercise completion, interruption, rejection, direct messages, text/file/data artifacts, chunked streams, and delayed resubscription. No TCK message-ID branches exist in the production service. This checks protocol interoperability; the durable actor/model behavior is covered by the Go integration tests and real-model demo.

The following exceptions remain with a2a-go v2.7.0 and this TCK revision. Paths below are relative to upstream `tests/compatibility/`; `[both]` means the jsonrpc and http_json parameterizations. The HTML report is not committed.

| Upstream test | Reason |
| --- | --- |
| `core_operations/test_push_notifications.py::TestPushNotificationCrud::test_create_push_config[jsonrpc]` | The TCK sends `task_id`; the SDK JSON decoder recognizes `taskId` only. ProtoJSON's alternate original field-name parsing is an SDK limitation. The five dependent JSON-RPC CRUD tests skip for the same reason. Production CRUD passes with the official Go SDK client. |
| `core_operations/test_task_history.py::TestHistoryLengthLimit::test_get_task_history_does_not_exceed_limit[jsonrpc]` | The same SDK limitation ignores the TCK's `history_length`. Camel-case `historyLength` is enforced and covered on both transports. |
| `core_operations/test_requirements.py::test_must_requirement[CORE-SEND-003-both]` | TCK registry entry omits `expected_error`, so its generic validator incorrectly expects success for the unsupported-media request. The server correctly returns ContentTypeNotSupportedError. |
| `core_operations/test_task_lifecycle.py::TestCancelTask::test_cancel_terminal_task_returns_error[http_json]` | SDK maps TaskNotCancelableError to HTTP 400; TCK expects 409. The error reason and JSON-RPC code are correct. |
| `core_operations/test_requirements.py::test_must_requirement[STREAM-SUB-004-both]` | SDK delivers the missing-task error inside an HTTP 200 SSE stream. TCK checks the initial HTTP response and treats it as success before reading the error. |
| `jsonrpc/test_sse_streaming.py::TestSseSubscribeToTask::test_subscribe_nonexistent_task_returns_error` | Same initial-response versus SSE-error mismatch. |
| `core_operations/test_task_lifecycle.py::TestSubscribeLifecycle::test_subscribe_rejects_terminal_task[both]` | SDK emits the correct unsupported-operation error as an SSE item; the TCK counts any item, including an error envelope, as a successful task event. The Go SDK client receives the expected sentinel. |
| `core_operations/test_task_lifecycle.py::TestSubscribeLifecycle::test_subscribe_terminates_at_terminal_state[both]` | The fixture starts at INPUT_REQUIRED. Per design section 1 and the SDK convention, our stream closes at interruption; the test expects it to remain open until a later terminal transition. |

Other skips cover unconfigured gRPC, unadvertised extended cards/required extensions, and negative tests whose unsupported capability cannot be exercised because streaming and push are enabled. Two content-type negative probes also skip because the SDK accepts their HTTP content type. These are not evidence of conformance. No complete TCK-conformance claim is made.

Inspector check: upstream `a2a-inspector` commit `8aa064639af106ff771d60428ef6d460f5454743` successfully validated the public card and initialized its JSON-RPC client. Through its running HTTP/Socket.IO backend, chat created a real contactd task (auth off, fake model), streamed a Task, status updates and a report artifact, all with empty validation-error lists; cancellation succeeded. The same check also completed a scripted SUT task. This verifies the Inspector backend's 1.0 interoperability; it is not a visual UI audit.

### Standards evidence

The design's section 18 checklist is covered as follows, subject to the explicit interoperability and crash-window exceptions above:

- [x] Card location, caching, required fields, both interfaces, auth declaration and extension: `TestVersionAndCardHTTP`, `TestAgentCardGoldens`.
- [x] Version enforcement, all methods, task/context identity and standard errors: `TestSDKTransports`, `TestCapabilityErrorViaSDK`, `TestLifecycleAndSnapshots`.
- [x] Blocking versus immediate sends, history limits, initial Task, terminal rejection and ordered broadcast: `TestSDKStreamsAndBlockingSend`, `TestBrokerOrderedBroadcastAndSlowSubscriber`, `TestScriptedTransports`.
- [x] Immutable terminal snapshots and deterministic whole-report artifacts: `TestLifecycleAndSnapshots`, `TestOutboxRetryRestartAndPushHeaders`, `TestWireFixtures`.
- [x] ProtoJSON enums, parts, timestamp serialization and golden wire fixtures: `TestWireFixtures` and card goldens.
- [x] Inline push configs, CRUD, idempotent deletion and exact callback headers: `TestSDKTransports`, `TestOutboxRetryRestartAndPushHeaders`.
- [x] Retry scheduling, restart replay, allowlists, redirect refusal and receiver authentication/deduplication: `TestOutboxTimerBackoff`, push tests and requester loop tests. The 24-hour retry budget resets on process restart, as documented above.
- [x] Scheduling, missed periods, busy checks, both kinds of expiry and bounded dispatch concurrency: `TestSchedulerTimers`, `TestSchedulerConcurrencyCap`.
- [x] SDK cross-check, TCK exceptions and Inspector check: tests and results described in this section.
