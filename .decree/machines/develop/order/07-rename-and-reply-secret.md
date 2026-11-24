---
machine: go_develop
---
# 07: Rename to decree-go-rest, and give the reply endpoint its own secret

## Overview

Two changes the user asked for:

1. **The project is `decree-go-rest`,** not `decree-api`. The repository directory is already renamed (`~/GitHub/decree-go-rest`). Everything else follows: module, binary, config file, environment variables, lock file, schema, systemd unit and docs.
2. **The answer endpoint takes its own secret.** `POST /runs/{wait_id}/replies/{event}` answers a question a run is waiting on, often an approval. So it gets its own bearer secret, like a configured endpoint, instead of always the default one. `GET /runs/{id}` gets the same option.

Migrations 01–06 are history and are not edited.

## Requirements

Read SPEC.md and the whole code base first.

1. **Rename** with `git mv` where a file moves:
   - module `github.com/jtmckay/decree-go-rest`, and every import;
   - `cmd/decree-api/` → `cmd/decree-go-rest/`; binary `decree-go-rest`;
   - default config file `./decree-go-rest.yml`; `decree-api.example.yml` → `decree-go-rest.example.yml`; `decree-api.schema.json` → `decree-go-rest.schema.json`, with its `$id` and the example's `$schema` line;
   - environment variables `DECREE_API_SECRET` → `DECREE_GO_REST_SECRET` and `DECREE_API_LISTEN` → `DECREE_GO_REST_LISTEN`;
   - lock file `<project>/.decree/decree-go-rest.lock`;
   - `deploy/decree-api.service` → `deploy/decree-go-rest.service`;
   - `.gitignore`;
   - log messages, `-version` output, the OpenAPI `info.title`;
   - SPEC.md and README.md throughout. In SPEC.md the history section keeps naming existential's `decree-webhook`.
   
   No old name stays as an alias: no fallback to `DECREE_API_*`, no second config filename.
2. **Built-ins as objects,** one shape per key, in SPEC.md §3 and §7, the config loader, the schema and the example:

   ```yaml
   builtins:
     status:  { enabled: true, secret_env: DECREE_GO_REST_STATUS_SECRET }   # secret_env optional: the top-level secret
     replies: { enabled: true, secret_env: DECREE_GO_REST_REPLY_SECRET }    # secret_env optional: the top-level secret
     openapi: { enabled: true }                                             # no auth
   ```

   - `enabled` defaults to `true`, and `secret_env` to the top-level one.
   - A bare `true`/`false` is a config error that names the object form (`builtins.replies: write { enabled: true }`).
   - The secret is validated like an endpoint's (§3 step 6: set, non-blank, at least 32 characters) when the built-in is enabled.
   - `openapi` takes no `secret_env`; it is a config error.
   - The example config gives `replies` its own `secret_env`, with a comment: answering a run's question, such as an approval, deserves its own secret.
   - SPEC.md §10 says the same in its security notes.
3. **Reload:** a changed built-in secret takes effect on reload, like an endpoint's.
4. **Tests:**
   - the reply endpoint accepts its own secret, and rejects the default one when its own is set (401);
   - the same for status;
   - each built-in falls back to the top-level secret when it has none;
   - a short or missing built-in secret is a startup error;
   - bare `true` and `openapi.secret_env` are config errors;
   - the OpenAPI document shows each built-in's security scheme;
   - every existing test still passes under the new names;
   - a test that no file outside `.decree/migrations/` and `.decree/runs/` contains `decree-api` or `DECREE_API_`.

- Only this migration's scope.
- SPEC.md is the source of truth, and this migration changes it as described. If anything is ambiguous, write the question to a file named `STOP` in the run directory (the directory that holds the message file you were given) and end without further changes.
- Standard library plus the existing dependencies only. Tests never call a real model or the network, and never touch this repository's own `.decree/`, except the end-to-end test as it already does.
- Print the evidence for each acceptance criterion at the end of your reply.
- The gate passes: `gofmt -l .` lists nothing, `go vet ./...` and `go test -race ./...` pass.

## Acceptance Criteria

- **Given** the repository
  **When** `rg -n 'decree-api|DECREE_API_' --glob '!.decree/migrations/**' --glob '!.decree/runs/**'` runs
  **Then** nothing matches

- **Given** `builtins.replies.secret_env` set to its own secret
  **When** a reply arrives with the default secret, and then with its own
  **Then** the first gets 401, the second 201

- **Given** `go build ./cmd/decree-go-rest`
  **When** the binary runs with `-check` on `decree-go-rest.example.yml`
  **Then** it validates the example (stub decree, temp project)
