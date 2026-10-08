---
machine: go_develop
---
# 02: Endpoints: route, authenticate, validate, emit

## Overview

Serve the configured endpoints: SPEC.md §4, steps 1, 2 and 4–7, and §10. Rate budgets come in 03.

## Requirements

Read SPEC.md §4, §10 and §12 first, and the code from 01.

1. An `http.Server` on `listen` (or `DECREE_API_LISTEN`), routing with `net/http` path patterns built from the config. Exactly one trailing slash is ignored. Unknown path: 404. Wrong method: 405 with `Allow`.
2. Bearer authentication per endpoint, constant time, lengths compared first (§4 step 2).
3. Path parameters: URL-decoded once, full match against the pattern (default `[A-Za-z0-9_\-!]+`), at most 200 bytes (§4 step 4).
4. Body: cap `max_body_bytes` (413), the endpoint's `body` rule, a trailing newline added to a non-empty body (§4 step 5).
5. Emit exactly as §4 step 6 says: argv, working directory, stdin, environment (no `DECREE_*`; `TRACEPARENT`/`TRACESTATE` only from a valid `traceparent`), umask `027`, 10 s timeout. Respond as §4 step 7. Every error body is `{"error": …}` JSON.
6. Graceful shutdown on SIGTERM and SIGINT: stop accepting, finish in-flight requests (up to 10 s).
7. Tests (`httptest` and the stub decree, which records argv, cwd, env and stdin):
   - golden argv and stdin for each endpoint of the example config;
   - each response status of §4;
   - a parameter outside its pattern is a 400 and is never passed to decree;
   - `DECREE_*` in the service's environment never reaches decree;
   - a valid `traceparent` reaches decree as `TRACEPARENT`, an invalid one does not;
   - the file mode the stub sees from umask.

- Only this migration's scope; later migrations build the rest of SPEC.md.
- SPEC.md is the source of truth. If it is ambiguous, or disagrees with how decree actually behaves (check with the `decree` binary and its `--help`), do not guess: write the question to a file named `STOP` in the run directory (the directory that holds the message file you were given) and end without further changes.
- Standard library plus `gopkg.in/yaml.v3` only. Go 1.22 or newer in `go.mod`.
- Tests never call a real model or the network, and never touch this repository's own `.decree/` (use temp directories and the stub `decree` of SPEC.md §12).
- Print the evidence for each acceptance criterion (test names or command output) at the end of your reply.
- The gate passes: `gofmt -l .` lists nothing, `go vet ./...` and `go test -race ./...` pass.

## Acceptance Criteria

- **Given** the example config and the stub decree
  **When** `POST /notify/backup` arrives with the right bearer and body `disk 3 is full`
  **Then** the stub saw `emit --machine notify --param title=backup --param priority=high --format json` in the project with stdin `disk 3 is full\n`, and the response is 201 with the id

- **Given** a wrong method, a bad bearer, a bad parameter and an oversized body
  **When** each arrives
  **Then** each gets 405, 401, 400 and 413, and none runs decree
