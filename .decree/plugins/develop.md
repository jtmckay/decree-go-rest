---
name: develop
extends: decree_develop
scripts:
  conventions.md: |
    # Go conventions (develop's implement, fix and verify prompts read this)

    - SPEC.md is the source of truth: change it in the same commit as the code it describes.
    - Format with `gofmt`; keep `go vet ./...` clean.
    - Standard library first; the only outside dependency is `gopkg.in/yaml.v3`, unless the
      message allows another.
    - Tests beside the code (`_test.go`); handler tests with `httptest` and the stub `decree`
      in `internal/decreetest`, never the network.
    - Wrap errors with context (`fmt.Errorf("...: %w", err)`); no `panic` outside `main` and tests.
    - `go test -race ./...` must pass before you end; run the narrowest package first while working.
  gate.sh: |
    #!/usr/bin/env bash
    # develop's gate: gofmt, go vet and go test -race, also kept in gate.log for fix. It
    # names `pass` if they all pass and `fail` if one fails; a non-zero exit, such as a
    # missing tool, means they could not run, and ends the run without fix.
    set -euo pipefail
    log="${DECREE_ROOT_RUN_DIR}/gate.log"

    missing() {
      echo "gate: $1 not found on PATH; the checks cannot run" | tee "${log}" >&2
      exit 1
    }
    command -v go >/dev/null || missing go
    command -v gofmt >/dev/null || missing gofmt

    checks() {
      unformatted=$(gofmt -l .)
      if [ -n "${unformatted}" ]; then
        echo "gofmt -l lists files that need formatting:"
        echo "${unformatted}"
        return 1
      fi
      go vet ./... &&
        go test -race ./...
    }

    if checks 2>&1 | tee "${log}"; then
      echo pass > "${DECREE_EVENT_FILE}"
    else
      echo fail > "${DECREE_EVENT_FILE}"
    fi
  requirements: |
    # What develop's gate needs, one resource per line, as in infra's requirements.
    #
    # kind    name   version   source
    command   go
    command   gofmt
---
# develop

decree-go-rest's develop: [`decree_develop`](https://github.com/jtmckay/decree/blob/main/plugins/decree_develop.md), extended with the Go gate (`gofmt -l`, `go vet`, `go test -race`) and Go conventions. Order files in `.decree/machines/develop/order/` implement SPEC.md one change at a time.
