---
machine: go_develop
---
# 08: No built-ins: every endpoint emits or replies, OpenAPI by environment

## Overview

The user does not want a `builtins` section. Decided:

- **Every endpoint is configured.** Each has an `action`, `emit` (the default) or `event`:
  - `emit` queues a new message with `decree emit`, as today;
  - `event` replies to a run waiting in a `person` state with `decree event`.
  
  So an approval endpoint is an ordinary endpoint with its own path, secret, patterns and rate budget, instead of the fixed `POST /runs/{wait_id}/replies/{event}` built-in.
- **`GET /runs/{id}` (status) is removed.**
- **`GET /openapi.json` is switched by the environment,** not the config: `DECREE_GO_REST_OPENAPI`.
- **`GET /healthz` stays,** always on, as it is today.

This is a breaking change to the config. Nothing old is accepted: a `builtins` key, or an endpoint mixing `message` and `reply`, is a config error that names the new form.

## Requirements

Read SPEC.md and the code base first.

1. **`action` on every endpoint**, SPEC.md §3, the loader, the schema and the example:

   ```yaml
   endpoints:
     - path: /notify/{title}                 # action: emit is the default
       patterns: { title: '[A-Za-z0-9 _.-]{1,80}' }
       message:
         machine: notify
         params: { title: '{{title}}', priority: high }

     # Answering a run's question, such as an approval, deserves its own secret.
     - path: /approve/{wait_id}
       action: event
       secret_env: DECREE_GO_REST_APPROVE_SECRET
       patterns: { wait_id: '[A-Za-z0-9._-]{1,128}' }
       body: optional                        # the note
       reply:
         to: '{{wait_id}}'
         event: approve
   ```

   - **`action: emit`** requires `message` (as today) and forbids `reply`.
   - **`action: event`** requires `reply` with `to` and `event`, and forbids `message`. Both are strings that may use `{{name}}` placeholders. A literal `event` must match decree's event-name pattern (`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)*$`). Every path parameter must be used, as for `emit`. The default pattern of a parameter used in `to` is `[A-Za-z0-9._-]{1,128}`, since wait ids contain `.`.
   - **`body` for an event endpoint** defaults to `optional`. The body is the note, passed as `-m=<body>` unchanged and omitted when empty (SPEC.md §7 today).
   - **The machine/params validation** of §3 steps 4–5 applies to `emit` endpoints only.
   - **Startup validation** of an `event` endpoint checks only its shape, since which runs are waiting changes over time.
2. **Handling** (SPEC.md §4, steps 6 and 7): an `event` endpoint runs `decree event <to> <event> [-m=<body>] --format json`, with the same working directory, environment rules, umask and timeout as `emit`. It responds:
   - exit 0: 201 with `{"id", "path", "to", "event"}`;
   - exit 1 (the run is not waiting, or does not accept the event): 409 with decree's message;
   - anything else: 500.
3. **Remove** the `builtins` config, `GET /runs/{id}` and the built-in `POST /runs/{wait_id}/replies/{event}`, with their code, tests and docs.
4. **OpenAPI by environment.**
   - `DECREE_GO_REST_OPENAPI` is `true` or `false`, read at startup; any other value is a startup error. Default `false`: the document lists every path, so serving it is opt-in.
   - When it is `true`, `GET /openapi.json` serves the document (no auth), generated from the loaded config and following reloads. It describes each endpoint by its action: the request body as `text/plain`, and the responses of §4 or of the `event` handling above.
   - When it is `false`, the path is a 404 like any unknown path.
   - `/healthz` and `/openapi.json` are reserved: a configured endpoint may not use either path (a startup error), whatever the variable says.
5. **SPEC.md:**
   - §3 (keys and validation), §4, and §7, which becomes "Health and OpenAPI";
   - §10: give approval endpoints their own secret;
   - §11: the environment variables, listed in one table with `DECREE_GO_REST_SECRET`, `DECREE_GO_REST_LISTEN` and `DECREE_GO_REST_OPENAPI`;
   - §13, the history table: the built-ins row becomes "event endpoints and opt-in OpenAPI".
   
   Update README.md, the example config, `decree-go-rest.schema.json` and the systemd unit, which documents `DECREE_GO_REST_OPENAPI`, to match.
6. **Tests:**
   - each `action` with its required and forbidden keys, and the error naming the new form for `builtins` or mixed keys;
   - an `event` endpoint's argv with and without a note, including a note that starts with `-`;
   - the 201, 409 and 500 responses;
   - an `event` endpoint's own secret: its secret works, and the top-level one gets 401;
   - `DECREE_GO_REST_OPENAPI` `true`, `false`, unset and invalid;
   - the reserved paths;
   - the OpenAPI golden file updated;
   - the end-to-end test extended: a `person` machine is waiting, a POST to an `event` endpoint replies, and the run finishes.

- Only this migration's scope.
- SPEC.md is the source of truth, and this migration changes it as described. If anything is ambiguous, write the question to a file named `STOP` in the run directory (the directory that holds the message file you were given) and end without further changes.
- Standard library plus the existing dependencies only. Tests never call a real model or the network, and never touch this repository's own `.decree/`.
- Print the evidence for each acceptance criterion at the end of your reply.
- The gate passes: `gofmt -l .` lists nothing, `go vet ./...` and `go test -race ./...` pass.

## Acceptance Criteria

- **Given** an `action: event` endpoint `/approve/{wait_id}` with its own secret, and a run waiting in a `person` state (end-to-end, real decree)
  **When** it gets a POST with that secret and a note
  **Then** the response is 201, and the run continues with `approve` and finishes

- **Given** a config with a `builtins` key
  **When** decree-go-rest starts or runs `-check`
  **Then** it fails, naming the endpoint `action` form

- **Given** `DECREE_GO_REST_OPENAPI` unset, `false`, `true` or `maybe`
  **When** decree-go-rest starts and `/openapi.json` is requested
  **Then** it is 404, 404, 200, or a startup error

- **Given** the repository
  **When** `rg -n 'builtins|/runs/\{id\}|replies/\{event\}' --glob '!.decree/**'` runs
  **Then** nothing matches, except history in SPEC.md §13
