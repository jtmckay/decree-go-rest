---
machine: develop
---
# 09: decree 0.6

## Overview

decree-go-rest runs decree 0.5. decree 0.6 changes four things it relies on:

- `decree event` is `decree reply`. It takes a wait id or a run id, an event and `-m <note>`. Its `--format json` prints `{"id", "path"}`, as `cli/reply.schema.json` says.
- A run waits for a reply in a `reply` state, not a `person` state.
- A machine is `.decree/machines/<name>/machine.yml`, not `.decree/machines/<name>.yml`. Its typed params are still its `data`.
- A project installs its plugins before the daemon runs: `decree install && decree daemon`.

Move decree-go-rest to decree 0.6, and follow 0.6's names, without legacy aliases.

## Requirements

- **`action: reply`.** The endpoint action that answers a waiting run is `reply`, matching decree's command and state; `action: event` is a config error that names `action: reply`. The `reply:` block (`to`, `event`) is unchanged. It runs `decree reply <to> <event> [-m=<body>] --format json`, and a 201 is `{"id", "path", "to", "event"}` as before. Exit 1 is a 409, as before.
- **Machines:** step 4 and 5 of validation read `.decree/machines/<machine>/machine.yml`.
- **Version:** step 7 requires decree 0.6.0 or newer. A 0.6.0 release candidate (`0.6.0-rc.N`) counts as 0.6. An older one is an error that names the version found and the one needed.
- **Stub and tests:** the stub `decree` in `internal/decreetest` reports `decree 0.6.0`, answers `reply` instead of `event`, and follows decree 0.6's `schema/v1/cli/` (emit, reply, check). The end-to-end test builds its temp project in the 0.6 layout: `machines/<name>/machine.yml` and `machines/<name>/scripts/`, a `reply` state in place of the `person` state, and `decree reply` through an endpoint of `action: reply`.
- **Packaging:** the `Dockerfile`'s `DECREE_TAG` default is `v0.6.0`. `example/compose.yml`'s daemon container runs `decree install && decree daemon`, and `example/daemon/Dockerfile` installs decree `v0.6.0`. The example config uses `action: reply`.
- **Docs:**
  - SPEC.md describes decree 0.6 throughout: the intro, §2, §3's table and validation, §4, §10, §11, §12. §13 gains a row for this change.
  - README.md matches it.
  - `decree-go-rest.schema.json` accepts `action: reply` and not `event`.

## Constraints

- No other behaviour changes.
- The only outside dependency stays `gopkg.in/yaml.v3`.
- If SPEC.md and decree 0.6 disagree in a way you cannot settle, write the question to `STOP` in the run directory and end without further changes.
- The gate passes: `gofmt -l .` lists nothing, and `go vet ./...` and `go test -race ./...` pass. The end-to-end test runs, not skipped, with the `decree` on `PATH` (0.6.0-rc.4 or newer).
- Print the evidence for each acceptance criterion at the end of your reply.

## Acceptance Criteria

- **Given** a config with an `action: reply` endpoint
  **When** it receives an authenticated POST with a note
  **Then** the stub records `reply <to> <event> -m=<note> --format json` and the response is 201

- **Given** a config with `action: event`
  **When** `decree-go-rest -check` runs
  **Then** it exits 1 with an error naming `action: reply`

- **Given** a machine at `.decree/machines/<m>/machine.yml` with `data`
  **When** an `emit` endpoint's params are validated
  **Then** unknown params and wrong types are errors as before, and a valid config passes

- **Given** a stub reporting `decree 0.5.0`, or `decree 0.6.0-rc.4`
  **When** step 7 runs
  **Then** the first is an error naming 0.6.0 and the second passes

- **Given** the real decree 0.6 on `PATH`
  **When** the end-to-end test runs
  **Then** a POST runs a machine to `done` with its params, and a reply endpoint answers a run waiting in a `reply` state, which finishes
