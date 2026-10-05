---
machine: go_develop
---
# 05: Run status, replies, and the OpenAPI document

## Overview

The rest of SPEC.md §7: `GET /runs/{id}`, `POST /runs/{wait_id}/replies/{event}` and `GET /openapi.json`, each switchable in `builtins`.

## Requirements

Read SPEC.md §7 and §12 first, and the code from 01–04.

1. `GET /runs/{id}`: default secret; `id` matches `[A-Za-z0-9._-]{1,128}`; runs `decree status <id> --format json` in the project; 200 with decree's document unchanged; 404 when decree exits 1; 500 otherwise.
2. `POST /runs/{wait_id}/replies/{event}`: default secret; both parameters checked against `[A-Za-z0-9._-]{1,128}`; runs `decree event <wait_id> <event> [-m <body>] --format json`; 201 with decree's `{id, path}`; 409 when decree exits 1, with decree's message; 500 otherwise. The body cap applies.
3. `GET /openapi.json`: an OpenAPI 3.1 document generated from the loaded config, as §7 describes. It follows reloads.
4. The startup error of §7 for a configured path under an enabled built-in's prefix.
5. Both built-ins pass through the same budgets, logging and `DECREE_*`/trace environment handling as configured endpoints.
6. Tests:
   - each status code, with the stub decree;
   - the OpenAPI document as a golden file for the example config;
   - a check that it is OpenAPI 3.1 in shape: `openapi: 3.1.0`, every configured path present, path parameters with their `pattern`, and the bearer security scheme;
   - a reload changing the document.

- Only this migration's scope; later migrations build the rest of SPEC.md.
- SPEC.md is the source of truth. If it is ambiguous, or disagrees with how decree actually behaves (check with the `decree` binary and its `--help`), do not guess: write the question to a file named `STOP` in the run directory (the directory that holds the message file you were given) and end without further changes.
- Standard library plus `gopkg.in/yaml.v3` only. Go 1.22 or newer in `go.mod`.
- Tests never call a real model or the network, and never touch this repository's own `.decree/` (use temp directories and the stub `decree` of SPEC.md §12).
- Print the evidence for each acceptance criterion (test names or command output) at the end of your reply.
- The gate passes: `gofmt -l .` lists nothing, `go vet ./...` and `go test -race ./...` pass.

## Acceptance Criteria

- **Given** a run waiting in a `person` state (the stub reports it)
  **When** `POST /runs/<wait id>/replies/approve` arrives with a note
  **Then** the stub saw `event <wait id> approve -m <note> --format json`, and the response is 201

- **Given** the example config
  **When** `GET /openapi.json` is requested
  **Then** it equals the golden document, and lists every endpoint with its parameters and patterns
