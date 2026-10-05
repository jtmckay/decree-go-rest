#!/usr/bin/env bash
# The gate: formatting, vet and tests (with the race detector), also kept in
# gate.log for qa.
set -euo pipefail
{
  unformatted=$(gofmt -l .)
  if [ -n "${unformatted}" ]; then
    echo "gofmt -l lists files that need formatting:"
    echo "${unformatted}"
    exit 1
  fi
  go vet ./... &&
    go test -race ./...
} 2>&1 | tee "${DECREE_RUN_DIR}/gate.log"
