---
machine: go_develop
---
# 01: Module, config loading and startup validation

## Overview

Start decree-api: the Go module, the config file of SPEC.md §3, and the full startup validation, reachable through `decree-api -check`. Nothing listens yet.

## Requirements

Read SPEC.md in full first, then §3 and §11 closely.

1. `go mod init` the module `github.com/jtmckay/decree-api` and lay out the code as `cmd/decree-api/` (main) plus `internal/` packages; config lives in `internal/config`.
2. Load `decree-api.yml` (`-config`, default `./decree-api.yml`) with every key, default and duration of §3. Durations use decree's format, a whole number and `s`, `m`, `h` or `d`, with one parser. Unknown keys are an error.
3. Implement every validation rule of §3, steps 1–7, reporting **all** errors at once, each naming the endpoint (by path) and the rule. Step 5 reads the machine's YAML `data` (`<project>/.decree/machines/<machine>.yml`). Step 7 runs the configured decree binary: `--version` (0.5 or newer), then `check --format json` in the project.
4. `decree-api -check` prints `config ok: <n> endpoints` or the errors, and exits 0 or 1.
5. Add `decree-api.example.yml` (the example of §3) and a `.gitignore` (`/decree-api`, `/dist`).
6. Tests: every default; every validation rule passing and failing (a table, one case per rule and failure); several errors reported together; the stub decree answers `--version` and `check`, including an invalid project and an old version.

- Only this migration's scope; later migrations build the rest of SPEC.md.
- SPEC.md is the source of truth. If it is ambiguous, or disagrees with how decree actually behaves (check with the `decree` binary and its `--help`), do not guess: write the question to a file named `STOP` in the run directory (the directory that holds the message file you were given) and end without further changes.
- Standard library plus `gopkg.in/yaml.v3` only. Go 1.22 or newer in `go.mod`.
- Tests never call a real model or the network, and never touch this repository's own `.decree/` (use temp directories and the stub `decree` of SPEC.md §12).
- Print the evidence for each acceptance criterion (test names or command output) at the end of your reply.
- The gate passes: `gofmt -l .` lists nothing, `go vet ./...` and `go test -race ./...` pass.

## Acceptance Criteria

- **Given** `decree-api.example.yml` with a temp project holding the machines it names and a stub decree
  **When** `decree-api -check` runs
  **Then** it prints `config ok: 3 endpoints` and exits 0

- **Given** a config breaking several rules at once
  **When** `decree-api -check` runs
  **Then** it exits 1 and lists every error
