---
machine: go_develop
---
# 03: Rate budgets, request logging and config reload

## Overview

SPEC.md §4 step 3, §6, §8 and §9: the two rate budgets in the order that makes them fair, structured logs, and validated hot reload.

## Requirements

Read SPEC.md §4, §6, §8 and §9 first, and the code from 01 and 02.

1. The two fixed-window budgets of §6, global. Authentication happens before either is consulted; rejected requests (routing, method, auth) spend from the failure budget only; authenticated requests (including their 400s) spend from the request budget only. 429 carries `Retry-After`.
2. `log/slog` JSON logs on stderr: one record per request with the fields of §9, using the route pattern, never the raw path. Never log secrets, bodies or parameter values. Startup records list the routes.
3. Reload as §8 says: mtime polling every 2 s, a 300 ms settle, full validation, an atomic swap of the route table (in-flight requests finish on the old one), and the old config kept plus the error recorded when the new one is invalid. Keys that need a restart log a warning.
4. Tests:
   - each budget property listed in SPEC.md §12;
   - logs contain no secret, body or parameter value (assert on captured output);
   - reload swaps on a valid change, keeps the old config on an invalid one, and records the error (exposed in 04's `/healthz`; here, through an internal accessor);
   - the race detector stays clean under concurrent requests during a reload.

- Only this migration's scope; later migrations build the rest of SPEC.md.
- SPEC.md is the source of truth. If it is ambiguous, or disagrees with how decree actually behaves (check with the `decree` binary and its `--help`), do not guess: write the question to a file named `STOP` in the run directory (the directory that holds the message file you were given) and end without further changes.
- Standard library plus `gopkg.in/yaml.v3` only. Go 1.22 or newer in `go.mod`.
- Tests never call a real model or the network, and never touch this repository's own `.decree/` (use temp directories and the stub `decree` of SPEC.md §12).
- Print the evidence for each acceptance criterion (test names or command output) at the end of your reply.
- The gate passes: `gofmt -l .` lists nothing, `go vet ./...` and `go test -race ./...` pass.

## Acceptance Criteria

- **Given** `rate_fail_max: 10` and eleven requests with a wrong bearer
  **When** a request with the right bearer follows
  **Then** the wrong ones end with 429, and the right one is 201

- **Given** a running server
  **When** its config file is replaced with an invalid one
  **Then** requests still use the old routes, and the error is recorded
