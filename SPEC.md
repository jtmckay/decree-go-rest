# decree-go-rest: specification

decree-go-rest is the HTTP front door of a [decree](https://github.com/jtmckay/decree) 0.5 project:

- endpoints, defined in one YAML file, turn authenticated `POST`s into decree inbox messages;
- an endpoint can instead reply to a run waiting for a person, such as an approval, through `decree event`;
- it keeps `decree daemon` running, so those messages are processed.

It runs no work itself. decree runs the work.

It succeeds the `decree-webhook` service in [jtmckay/existential](https://github.com/jtmckay/existential/tree/main/services/automation/webhook) (`services/automation/webhook`), which did the same for decree 0.4. Every behaviour that service settled for a reason is kept. What changed is listed in [Differences from existential's decree-webhook](#differences-from-existentials-decree-webhook).

This file is the source of truth while the service is built. The migrations in `.decree/migrations/` implement it, one section at a time. A migration that finds this spec ambiguous stops and asks (the `STOP` file) rather than guessing.

## 1. Goals and non-goals

**Goals:**

- Endpoints are configuration, not code. Adding one means editing `decree-go-rest.yml`; there is no route table in the code.
- **Every message is written by decree:** `decree emit`, or `decree event` for a reply, so ids, atomic writes, validation of the machine and its typed `params`, and trace context are decree's, not reimplemented.
- **Least privilege in a container:** decree-go-rest can only queue messages, with `.decree/` read-only but for `inbox/`; `decree daemon` runs beside it, with the tools and access its scripts need. Outside a container, decree-go-rest can supervise the daemon itself, as one process to run.
- **Safe to expose behind a TLS reverse proxy:** per-endpoint bearer secrets, request limits, rate budgets that unauthenticated traffic cannot exhaust for real callers.
- **Standard, discoverable:** an OpenAPI 3.1 document generated from the config, JSON responses, W3C Trace Context passed through to the run.

**Non-goals:**

- No parsing of request bodies (they are opaque text, as before).
- No dashboard.
- No database.
- No TLS (terminate it in Caddy or similar).
- No users, sessions or OAuth.
- No running scripts.

## 2. Architecture

```text
client --POST /notify/backup--> decree-go-rest --exec--> decree emit --machine notify --param title=backup
                                    |                        |
                                    | supervises             v  writes .decree/inbox/<id>.md
                                    v                        |
                              decree daemon  <---------------+  claims it, runs machine `notify`
```

decree-go-rest is one static Go binary (Go 1.22 or newer, for `net/http` path patterns). Its only dependency outside the standard library is `gopkg.in/yaml.v3`. It runs `decree` (0.5 or newer, found on `PATH` or at `decree:` in the config) as a child process. It never writes messages or runs itself; the only file it creates in `.decree/` is its lock (§5).

## 3. Configuration: `decree-go-rest.yml`

The binary reads the file named by `-config` (default `./decree-go-rest.yml`). A full example:

```yaml
listen: 127.0.0.1:8801 # keep it on localhost behind a reverse proxy
secret_env: DECREE_GO_REST_SECRET # the default bearer secret, read from this environment variable
decree: decree # the decree binary; a name on PATH or a path

daemon:
  enabled: false # true supervises `decree daemon`; in Docker it runs in its own container (compose.yml)
  interval: 2s # passed as --interval

limits:
  max_body_bytes: 262144
  rate_window: 60s
  rate_max: 60 # authenticated requests per window, all endpoints together
  rate_fail_max: 10 # rejected requests per window (bad auth, unknown path, wrong method)

endpoints:
  - path: /notify
    message:
      machine: notify
      params: { title: Untitled, priority: high }

  - path: /notify/{title} # action: emit is the default
    patterns:
      title: "[A-Za-z0-9 _.-]{1,80}"
    message:
      machine: notify
      params:
        title: "{{title}}"
        priority: high

  - path: /comfy/{type}/{name}
    secret_env: COMFY_SECRET # this endpoint's own secret
    patterns: { type: "[a-z0-9-]+", name: "[A-Za-z0-9_.-]+" }
    body: required # required (default), optional or none
    message:
      machine: comfy_image
      params: { workflow: "{{type}}", output_prefix: "comfy/{{name}}" }

  # Answering a run's question, such as an approval, deserves its own secret.
  - path: /approve/{wait_id}
    action: event # reply to a run waiting in a person state
    secret_env: DECREE_GO_REST_APPROVE_SECRET
    patterns: { wait_id: "[A-Za-z0-9._-]{1,128}" }
    body: optional # the note
    reply:
      to: "{{wait_id}}"
      event: approve
```

It is [`example/.decree/decree-go-rest.yml`](example/.decree/decree-go-rest.yml) in the repository, beside the `compose.yml` that runs it ([§11](#11-command-line-and-packaging)).

**Keys.** Every key is optional except `endpoints`, an `emit` endpoint's `message.machine`, an `event` endpoint's `reply.to` and `reply.event`, and a resolvable secret. The key of the removed built-in endpoints ([§13](#13-differences-from-existentials-decree-webhook)) is not accepted: it is a config error that names the endpoint `action` form. Durations use decree's format: a whole number and `s`, `m`, `h` or `d`.

| Key | Default | Meaning |
| --- | --- | --- |
| `project` | the config's directory, or its parent when the config is in `.decree/` | Directory holding `.decree/`, relative to the config file. |
| `listen` | `127.0.0.1:8801` | Address to listen on. Overridden by `DECREE_GO_REST_LISTEN` if set. |
| `secret_env` | `DECREE_GO_REST_SECRET` | Environment variable holding the default bearer secret. Secrets never appear in the file, so it can be committed. |
| `decree` | `decree` | The decree binary. |
| `daemon.enabled` / `daemon.interval` | `true` / `2s` | Supervise `decree daemon --interval <interval>`. `DECREE_GO_REST_DAEMON` overrides `enabled` ([§11](#11-command-line-and-packaging)); the container image turns it off. |
| `limits.*` | as above | Request body cap, and the two rate budgets ([§6](#6-limits)). |
| `endpoints[].path` | | `/` followed by literal segments and `{name}` segments. One full segment per parameter. |
| `endpoints[].action` | `emit` | `emit`: queue a new message with `decree emit` ([§4](#4-handling-a-request)); requires `message` and forbids `reply`. `event`: reply to a run waiting in a `person` state with `decree event`; requires `reply` and forbids `message`. |
| `endpoints[].secret_env` | the top-level one | This endpoint's secret. |
| `endpoints[].patterns` | | Per-parameter regular expression (Go RE2), anchored automatically. It replaces the default pattern: `[A-Za-z0-9._-]{1,128}` for a parameter an `event` endpoint uses in `reply.to`, since wait ids contain `.`; `[A-Za-z0-9_\-!]+` for any other. |
| `endpoints[].body` | `required` for `emit`, `optional` for `event` | `required`: a blank body is a 400. `optional`: an empty body is allowed. `none`: any body is a 400. An `event` endpoint's body is the note. |
| `endpoints[].message.machine` | | `emit`: the decree machine, `machines/<name>.yml` in the project. |
| `endpoints[].message.params` | | `emit`: values for the machine's `data`. A string may contain `{{name}}` placeholders for path parameters. |
| `endpoints[].reply.to` | | `event`: the wait id of the run to reply to, a string that may contain `{{name}}` placeholders, such as `'{{wait_id}}'`. |
| `endpoints[].reply.event` | | `event`: the event the run takes, a string that may contain `{{name}}` placeholders. A literal must match decree's event-name pattern, `^[a-z][a-z0-9_]*(\.[a-z0-9_]+)*$`. |

**Validation.** Every failure here is fatal at startup: the process exits non-zero and lists every error. Nothing serves until all of them pass.

1. **Path shape:** `^/[A-Za-z0-9._\-/{}]+$`, no `..`, no empty segments, every `{name}` a full segment matching `\w+`. No duplicate paths, and no two paths that conflict as `net/http` patterns. `/healthz` and `/openapi.json` are reserved ([§7](#7-health-and-openapi)): no path is either, or under either, whatever `DECREE_GO_REST_OPENAPI` says.
2. **Patterns:** every `patterns` key is a parameter of its path, and every pattern compiles.
3. **Action and placeholders:** `action` is `emit` or `event`, with its required key and without the other's; a mix names the form needed. Every `{{name}}` in `params` (`emit`) or in `reply.to` and `reply.event` (`event`) is a parameter of the path. Placeholders appear only in string values. Every path parameter is used at least once, so a parameter cannot be silently dropped. An `event` endpoint has a non-empty `reply.to` and `reply.event`, and a literal `reply.event` matches decree's event-name pattern.
4. **Machine** (`emit` only): `message.machine` names a machine file in the project.
5. **Params** (`emit` only): every `params` key is in that machine's `data` (read its YAML). A literal value matches the data's type (`string`, `int`, `bool`). A value with a placeholder requires `string` data.
6. **Secrets:** each referenced environment variable is set, non-blank, and at least 32 characters long.
7. **decree:** the `decree` binary runs, `decree --version` reports 0.5 or newer, and `decree check --format json` in the project reports `"valid": true`.

An `event` endpoint is checked for its shape only: which runs are waiting changes over time.

`decree-go-rest -check -config <file>` runs exactly this validation, prints the result and exits 0 or 1, without listening.

## 4. Handling a request

The order is fixed. It is what makes the rate budgets fair ([§6](#6-limits)).

1. **Route.** Match the path (exactly one trailing slash is ignored) against the endpoints, `/healthz` and, when it is on, `/openapi.json`.
   - An unknown path is a 404, charged to the failure budget.
   - A known path with the wrong method is a 405 with `Allow`, also charged to the failure budget.
2. **Authenticate.** `Authorization: Bearer <secret>`, compared in constant time, lengths checked first. Missing, malformed or wrong: 401, charged to the failure budget.
3. **Budget.** An authenticated request spends from `rate_max`. When it is exhausted: 429 with `Retry-After`.
4. **Validate parameters.** Each is URL-decoded once, then must fully match its pattern and be at most 200 bytes. Otherwise: 400.
5. **Read the body,** at most `max_body_bytes`. Over the cap: 413. Then apply the endpoint's `body` rule (400 when violated). The body is opaque bytes; for `emit`, a non-empty body gets a trailing newline if it lacks one.
6. **Emit** (`action: emit`). Run, with no shell:

   ```text
   decree emit --machine <machine> --param <key>=<value>... --format json
   ```

   - **Working directory:** the project root.
   - **Stdin:** the body.
   - **Params:** each takes its value with placeholders substituted, in the order the config lists them.
   - **Environment:** the service's, minus every `DECREE_*` variable, so a decree-go-rest started from a decree script cannot pass a run's identity on. `TRACEPARENT` and `TRACESTATE` are set from the request's `traceparent` and `tracestate` headers when they are valid W3C Trace Context, and removed otherwise, so the run joins the caller's trace.
   - **Mode:** the service runs with umask `027`, so messages, which may carry secrets, are not world-readable.
   - **Timeout:** 10 s.

   **Reply** (`action: event`). Run, with no shell:

   ```text
   decree event <to> <event> [-m=<body>] --format json
   ```

   - `<to>` and `<event>` are `reply.to` and `reply.event` with their placeholders substituted.
   - **The note** is the body unchanged (no newline added), passed as one argv entry, `-m=<body>`, so a note that starts with `-` is never read as an option. An empty body passes no `-m`. A body holding a NUL byte, which argv cannot carry, is a 400. Nothing goes to stdin.
   - **Working directory, environment, mode and timeout:** as for `emit`.
7. **Respond.**

   | `decree emit` | Response |
   | --- | --- |
   | exit 0 | 201 with `{"id": …, "path": …, "machine": …}`, from emit's JSON plus the machine |
   | exit 1 (a param of the wrong type, a depth limit…) | 400 with `{"error": "<decree's stderr message>"}` |
   | anything else, or the timeout | 500 with `{"error": "could not queue the message"}`; the details are logged, not returned |

   | `decree event` | Response |
   | --- | --- |
   | exit 0 | 201 with `{"id": …, "path": …, "to": …, "event": …}`, from event's JSON plus the wait id and event replied |
   | exit 1 (the run is not waiting, or does not accept the event) | 409 with `{"error": "<decree's stderr message>"}` |
   | anything else, or the timeout | 500 with `{"error": "could not queue the reply"}`; the details are logged, not returned |

Every error response is JSON, `{"error": "<message>"}`, with `Content-Type: application/json`.

## 5. The daemon

When `daemon.enabled` is true:

- **Start.** decree-go-rest starts `decree daemon --interval <interval>` in the project after validation and before it listens.
- **Logs.** The daemon's stdout and stderr go to decree-go-rest's log, one record per line, tagged `source=daemon` and `stream=stdout|stderr`.
- **Restart.** If the daemon exits, decree-go-rest restarts it after a back-off (1 s, doubling to 60 s at most). The back-off resets once the daemon has run for 5 minutes. Each restart is logged with the exit status.
- **Shutdown.** On SIGTERM or SIGINT, decree-go-rest stops accepting requests and finishes in-flight ones (up to 10 s). It then sends SIGTERM to the daemon and waits up to 15 s; decree then interrupts the running script, records `interrupted` and exits. Only after that does it send SIGKILL.
- **One daemon only.** decree-go-rest never runs two daemons for one project. It holds an exclusive `flock` on `<project>/.decree/decree-go-rest.lock` while it runs, and a second decree-go-rest for the same project exits with an error.

When it is false, decree-go-rest takes no lock and writes nothing in `.decree/` but the messages `decree emit` and `decree event` write to `inbox/`, so `.decree/` may be read-only apart from `inbox/`. Something else runs `decree daemon`; decree's own lock keeps that to one.
- **`/healthz`** reports the daemon's state ([§7](#7-health-and-openapi)).

A config change never restarts the daemon, because that would interrupt the running script and leave the run waiting for `decree process --retry`.

## 6. Limits

- **Body.** Over `max_body_bytes`: 413.
- **Rate.** There are two fixed-window budgets, global rather than per IP (behind a proxy every request has one source address):
  - **failures** (`rate_fail_max`): spent only by requests that fail routing, the method check or authentication. This is the brute-force brake.
  - **all** (`rate_max`): spent only by requests that authenticated. A 400 from an authenticated caller is a client bug, not an attack, so it spends from this budget, not the failure budget.

  Authentication happens before either budget is consulted. So unauthenticated traffic can exhaust only the failure budget, and can never lock out a caller holding the right secret. existential learned this the hard way: ten bad requests a minute took its whole pipeline down.
- **When the failure budget is exhausted,** rejected requests get 429 instead of 401, 404 or 405. Authenticated requests are unaffected.

## 7. Health and OpenAPI

Besides the configured endpoints, decree-go-rest serves two paths. Neither takes auth, and a configured endpoint may use neither, nor a path under either (a startup error), whatever `DECREE_GO_REST_OPENAPI` says.

| Endpoint | Auth | Does |
| --- | --- | --- |
| `GET /healthz` | none | Always served. 200 `{"ok": true, "daemon": {"enabled", "running", "pid", "restarts", "since"}, "config": {"loaded_at", "error": null}}`. 503 with the same body when the daemon is enabled but not running, or the last config reload failed. Exempt from rate limits. |
| `GET /openapi.json` | none | Served only when `DECREE_GO_REST_OPENAPI` is `true` ([§11](#11-command-line-and-packaging)); otherwise a 404 like any unknown path. An OpenAPI 3.1 document of every configured endpoint, `/healthz` and itself: paths, path parameters with their patterns, the bearer scheme, request bodies as `text/plain`, and each endpoint's responses by its action ([§4](#4-handling-a-request), steps 6 and 7). Generated from the loaded config, so it follows reloads. Exempt from rate limits. |

Serving the document is opt-in because it lists every path.

## 8. Reloading the config

- **When.** decree-go-rest polls the config file's modification time every 2 s, polling rather than watching, because editors and bind mounts replace the inode.
- **What it does.** On a change it waits 300 ms, so it never reads a half-written file, then loads and fully validates the new config ([§3](#3-configuration-decree-go-restyml), steps 1–6; step 7 only when `decree` or `project` changed).
- **If the new config is valid,** decree-go-rest swaps the route table atomically: requests in flight finish on the old one. The new table reads every secret again, so a changed `secret_env` takes effect. The daemon is not restarted.
- **If it is invalid,** decree-go-rest keeps serving the old config, logs every error, and reports it in `/healthz` until a valid config loads.
- **What reload cannot change:** `listen`, `project` and `daemon.*` take effect only on restart, and decree-go-rest logs a warning saying so. The environment (§11) is read at startup only.

## 9. Logging and tracing

- **Format.** Logs are JSON lines on stderr (`log/slog`).
- **One record per request:** `method`, `route` (the configured pattern, never the raw path, so parameter values stay out of logs), `status`, `duration_ms`, `message_id` when a message or reply was queued, and `trace_id` when the request carried a valid `traceparent`.
- **Never logged:** secrets, bodies (notes included) and parameter values, so neither a wait id nor an event taken from the path.
- **Startup records** list every route with its action, the machine an `emit` endpoint emits to, and the daemon command line.
- **Traces.** decree writes the trace itself (`runs/<id>/traces.jsonl`). decree-go-rest only passes `traceparent` on, so a caller that traces its request sees the decree run as part of the same trace.

## 10. Security

decree's [SECURITY.md](https://github.com/jtmckay/decree/blob/main/SECURITY.md) applies in full. A message is an instruction to the machines it names, and decree's built-in machines run Claude with your permissions. So an endpoint secret is as powerful as shell access to whatever those machines do. Hence:

- **Secrets** come only from the environment and are at least 32 characters. Generate one with `openssl rand -hex 32`. Give an endpoint that reaches a powerful machine its own `secret_env`.
- **Approval endpoints get their own secret.** An `action: event` endpoint answers a question a run is waiting on, often an approval. Answering a run's question, such as an approval, deserves its own secret: give each `event` endpoint its own `secret_env`, as the example's `/approve/{wait_id}` does with `DECREE_GO_REST_APPROVE_SECRET`, so a caller that may queue messages cannot also approve them.
- **Listen on localhost by default.** Expose the service only through a TLS reverse proxy.
- **Path parameters reach decree only as argv entries,** `--param` values or `decree event`'s `<to>` and `<event>`: no shell, and restricted by their pattern. Message bodies reach decree only on stdin; a note only as the one argv entry `-m=<body>`.
- **No endpoint can choose the machine:** `message.machine` is fixed in the config, and a placeholder is not allowed there.
- **The OpenAPI document is off by default.** It reveals paths, not secrets, but lists every one; set `DECREE_GO_REST_OPENAPI=true` only where that is wanted.

## 11. Command line and packaging

```text
decree-go-rest [-config decree-go-rest.yml] [-check] [-healthcheck]
```

- **`-check`:** validate the config ([§3](#3-configuration-decree-go-restyml)) and exit.
- **`-healthcheck`:** request `GET /healthz` on the configured listen address, then exit 0 on 200 and 1 otherwise. This is for container health checks with no curl.
- **Environment variables,** read at startup:

  | Variable | Default | Meaning |
  | --- | --- | --- |
  | `DECREE_GO_REST_SECRET` | | The default bearer secret, at least 32 characters. Its name is the config's `secret_env`; each endpoint's own `secret_env` names another variable. |
  | `DECREE_GO_REST_LISTEN` | | Overrides `listen` when set and non-empty. |
  | `DECREE_GO_REST_DAEMON` | | Overrides `daemon.enabled` when set and non-empty: `true` or `false`. Any other value is a config error. |
  | `DECREE_GO_REST_OPENAPI` | `false` | `true` serves `GET /openapi.json`; `false`, unset or empty does not. Any other value is a startup error. |
- **Releases:** the container image below is the supported way to run decree-go-rest. `.github/workflows/docker.yml` publishes it to `ghcr.io/jtmckay/decree-go-rest`: `X.Y.Z`, `X.Y` and `latest` for a `vX.Y.Z` tag, `main` and `sha-<commit>` for the main branch. The binary inside is static (`CGO_ENABLED=0`) and reports the tag or commit with `-version`.
- **`example/`:** `.decree/decree-go-rest.yml`, the example config of §3, and `compose.yml`: decree-go-rest from the image, with `.decree/` read-only but for `inbox/`, and `decree daemon` in a container of its own, built from `example/daemon/Dockerfile`, with the project read-write.
- **`decree-go-rest.schema.json`:** a JSON Schema of the config, so an editor completes keys. Point a config at it with `# yaml-language-server: $schema=…`, as decree's machines do.
- **Container image.** The `Dockerfile` ships decree-go-rest and the `decree` binary its endpoints run. It reads `/project/.decree/decree-go-rest.yml` and sets `DECREE_GO_REST_DAEMON=false`: the daemon runs the project's scripts, which need their own tools and write access to the project, so it runs in a container of its own. Its stop grace period must cover the 10 s of in-flight requests ([§5](#5-the-daemon)); the example gives it 15 s.

## 12. Testing

- **Stub `decree`.** Unit and handler tests (`httptest`) use a stub `decree` executable written into a temp directory. It records its argv, working directory, environment and stdin, and answers as the real one does for `emit`, `event`, `check`, `--version` and `daemon`. Each response follows decree's `--format json` schemas, in decree's `schema/v1/cli/`.
- **Golden tests** for the exact `decree emit` argv and stdin per endpoint, and for the OpenAPI document. Tests of the exact `decree event` argv, with and without a note.
- **Properties of the old service, kept as tests:**
  - authentication before budgets;
  - neither budget can starve the other;
  - a 400 from an authenticated caller does not spend the failure budget;
  - a 405 for a wrong method;
  - one trailing slash ignored;
  - a parameter with characters outside its pattern rejected, not repaired;
  - `DECREE_*` never passed to decree;
  - `traceparent` passed only when valid.
- **Supervision tests** with a stub daemon: restart with back-off, back-off reset, graceful SIGTERM, then SIGKILL after the timeout, and the lock refusing a second instance.
- **Reload tests:** an atomic swap on a valid change; old config kept and `/healthz` 503 on an invalid one; the daemon untouched.
- **End-to-end test** (skipped with a note when `decree` is not on `PATH`): a temp project with one machine whose script records its `DECREE_DATA_*`. decree-go-rest with the real decree and daemon receives a POST, and the test waits until the run is `done` and checks the recorded params. Then a run of a machine with a `person` state waits; a POST with a note to an `event` endpoint, on its own secret, replies `approve`, and the run finishes.
- **The gate** runs `gofmt -l`, `go vet ./...` and `go test -race ./...`.

## 13. Differences from existential's decree-webhook

| decree-webhook (decree 0.4) | decree-go-rest (decree 0.5) | Why |
| --- | --- | --- |
| Writes the inbox file itself: `routine:` plus free frontmatter keys | Runs `decree emit --machine … --param …` | 0.5 names `machine:`, and passes data as typed `params` validated against the machine's `data`. Free frontmatter keys no longer reach scripts. |
| Filename `<routine>-<HHMMSS>.md`, `O_EXCL` and a discriminator for same-second collisions | decree's ids (`YYYYMMDDTHHMMSSZ-xxxxxx`), written atomically by `emit` | 0.5's ids are unique and its writer is atomic, so the collision workaround and the chain-number rule (`-0`, at most 100) are gone. |
| Frontmatter built as a YAML node tree to block injection | Values passed as argv to `emit`; decree writes the YAML | The injection boundary is now decree's serializer, and there is no shell. |
| `secret:` in the config file (rendered from a template) | `secret_env:` names an environment variable | The config can be committed; secrets live in the environment. |
| Wrong method is a 404 (Express parity) | 405 with `Allow` | No legacy callers to keep compatible with. Both are charged to the failure budget. |
| Exit on config change; the container restarts | Validated atomic swap; the old config is kept if the new one is invalid | No dropped requests, and when decree-go-rest supervises the daemon, restarting it would interrupt the running script. |
| A separate container for the daemon | Still a separate container in Docker; decree-go-rest supervises `decree daemon` when `daemon.enabled` is true | The daemon's image is the project's, with its scripts' tools; the request-facing container stays minimal and can only queue messages. Outside Docker, supervising it keeps one process to run. |
| Inbox only | Event endpoints and opt-in OpenAPI: an endpoint with `action: event` replies to a run waiting in a `person` state, and `GET /openapi.json` is served when `DECREE_GO_REST_OPENAPI=true` | 0.5's `person` replies make approvals natural over HTTP, each on its own path, secret, patterns and rate budget. This replaced built-ins (a `builtins` config with `GET /runs/{id}` and `POST /runs/{wait_id}/replies/{event}`), removed in migration 08: every endpoint is configured. |
| No tracing | `traceparent` passed through to the run | decree 0.5 runs are OpenTelemetry traces. |

**Kept unchanged:**

- routes defined in configuration;
- opaque bodies;
- per-endpoint bearer secrets of 32 or more characters, compared in constant time;
- restricted path parameters (reject, never repair);
- one trailing slash ignored;
- the two global rate budgets with authentication first;
- body cap;
- startup validation that refuses to serve a half-working route table;
- a health check that needs no curl;
- golden tests of what reaches decree.
