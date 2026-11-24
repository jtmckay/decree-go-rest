---
machine: go_develop
---
# 04: Supervise decree daemon, and /healthz

## Overview

SPEC.md §5 and the `/healthz` row of §7: decree-api keeps `decree daemon` running for its project, and reports on it.

## Requirements

Read SPEC.md §5, §7 and §12 first, and the code from 01–03.

1. The single-instance lock of §5: an exclusive `flock` on `<project>/.decree/decree-api.lock`; a second instance exits with an error naming the lock.
2. When `daemon.enabled`, start `decree daemon --interval <interval>` in the project after validation and before listening. Its stdout and stderr are logged line by line, as §5 says. Restart it with the back-off of §5 and reset that back-off after 5 minutes up.
3. Shutdown order of §5: stop accepting, finish requests, SIGTERM the daemon, wait 15 s, then SIGKILL.
4. `GET /healthz` exactly as §7: no auth, exempt from rate limits, 200 or 503 with the daemon and config state.
5. `decree-api -healthcheck` (§11).
6. Tests with a stub daemon (a script that records its argv, can exit on demand, and handles SIGTERM, or ignores it in one case):
   - the start arguments;
   - restart after exit, with the back-off and its reset (inject a clock);
   - the graceful stop, and SIGKILL after the timeout;
   - the lock refusing a second instance;
   - `/healthz` 503 while the daemon is down and after a failed reload;
   - `-healthcheck` exit codes.

- Only this migration's scope; later migrations build the rest of SPEC.md.
- SPEC.md is the source of truth. If it is ambiguous, or disagrees with how decree actually behaves (check with the `decree` binary and its `--help`), do not guess: write the question to a file named `STOP` in the run directory (the directory that holds the message file you were given) and end without further changes.
- Standard library plus `gopkg.in/yaml.v3` only. Go 1.22 or newer in `go.mod`.
- Tests never call a real model or the network, and never touch this repository's own `.decree/` (use temp directories and the stub `decree` of SPEC.md §12).
- Print the evidence for each acceptance criterion (test names or command output) at the end of your reply.
- The gate passes: `gofmt -l .` lists nothing, `go vet ./...` and `go test -race ./...` pass.

## Acceptance Criteria

- **Given** a stub daemon that exits after one second
  **When** decree-api runs for a few seconds
  **Then** the daemon was restarted with growing delays, and `/healthz` reported 503 while it was down

- **Given** SIGTERM to decree-api
  **When** it shuts down
  **Then** the daemon got SIGTERM after in-flight requests finished, and SIGKILL only after 15 s without exiting
