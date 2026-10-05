---
machine: go_develop
---
# 06: README, config schema, systemd unit, and an end-to-end test with real decree

## Overview

SPEC.md §11 and the end-to-end test of §12: make decree-api installable and documented, and prove it with the real decree.

## Requirements

Read SPEC.md in full, the code from 01–05, and `decree --help`.

1. `README.md`:
   - what decree-api is, in one paragraph, linking SPEC.md;
   - install (`go install`, or `CGO_ENABLED=0 go build`);
   - a quick start: a decree project, a config, a secret from `openssl rand -hex 32`, run, `curl`;
   - the endpoints of §7;
   - the security notes of §10.
2. `decree-api.schema.json`: a JSON Schema (draft 2020-12) of the config, with a `description` on every key, and the `$schema` line in `decree-api.example.yml`. A test validates the example against it. This needs a JSON Schema validator; if none exists without a new dependency, add `github.com/santhosh-tekuri/jsonschema/v6` as a test-only dependency and say so.
3. `deploy/decree-api.service`: the systemd user unit of §11, with comments.
4. The end-to-end test of §12, skipped with a printed note when `decree` is not on `PATH`:
   - a temp decree project (`decree init`, plus one machine `echo_params` whose script writes its `DECREE_DATA_*` to a file);
   - decree-api with the real decree and daemon;
   - one POST, then wait (up to 30 s) until `GET /runs/{id}` reports `status: finished` and state `done`;
   - check the recorded params;
   - shut down and check the daemon exited.
5. `-version` prints decree-api's version, from `debug.ReadBuildInfo` or a linker flag.

- Only this migration's scope; later migrations build the rest of SPEC.md.
- SPEC.md is the source of truth. If it is ambiguous, or disagrees with how decree actually behaves (check with the `decree` binary and its `--help`), do not guess: write the question to a file named `STOP` in the run directory (the directory that holds the message file you were given) and end without further changes.
- Standard library plus `gopkg.in/yaml.v3` only. Go 1.22 or newer in `go.mod`.
- Tests never call a real model or the network, and never touch this repository's own `.decree/` (use temp directories and the stub `decree` of SPEC.md §12).
- Print the evidence for each acceptance criterion (test names or command output) at the end of your reply.
- The gate passes: `gofmt -l .` lists nothing, `go vet ./...` and `go test -race ./...` pass.

## Acceptance Criteria

- **Given** the real decree on `PATH`
  **When** the end-to-end test runs
  **Then** a POST becomes a finished run whose script saw the configured params

- **Given** `decree-api.example.yml`
  **When** it is validated against `decree-api.schema.json`
  **Then** it passes
