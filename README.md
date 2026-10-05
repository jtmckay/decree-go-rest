# decree-api

decree-api is the HTTP front door of a [decree](https://github.com/jtmckay/decree) 0.5 project. Endpoints defined in one YAML file turn authenticated `POST`s into decree inbox messages, always through `decree emit`, so ids, validation of the machine and its typed `params`, and trace context stay decree's. It keeps `decree daemon` running, so those messages are processed, and a few built-in endpoints read a run's status and reply to a run waiting for a person. It runs no work itself: decree does. The design, and the source of truth for every behaviour below, is [SPEC.md](SPEC.md).

## Install

Needs Go 1.22 or newer to build, and `decree` 0.5 or newer on `PATH` (or at `decree:` in the config) to run.

```sh
go install github.com/jtmckay/decree-api/cmd/decree-api@latest
```

or, from a checkout, a static binary:

```sh
CGO_ENABLED=0 go build -o decree-api ./cmd/decree-api
```

`decree-api -version` prints the version it was built from.

## Quick start

A decree project with one machine, `notify`, whose script sees the request's parameters as `DECREE_DATA_*`:

```sh
mkdir hello && cd hello
decree init
mkdir -p .decree/scripts/notify
cat > .decree/machines/notify.yml <<'EOF'
# yaml-language-server: $schema=../schema/v1/machine.schema.json
name: notify
description: Log a notification.
initial: send
data:
  title:    { type: string, default: Untitled }
  priority: { type: string, default: low }
states:
  send:   { invoke: send, transitions: { done: done } }
  done:   { final: true }
  failed: { final: true }
EOF
cat > .decree/scripts/notify/send <<'EOF'
#!/bin/sh
echo "notify: $DECREE_DATA_TITLE ($DECREE_DATA_PRIORITY)"
cat "$DECREE_MESSAGE"
EOF
chmod +x .decree/scripts/notify/send
decree graph && decree check
```

A config, `decree-api.yml`, next to `.decree/`:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/jtmckay/decree-api/main/decree-api.schema.json
endpoints:
  - path: /notify/{title}
    message:
      machine: notify
      params: { title: '{{title}}', priority: high }
```

A secret, then run it:

```sh
export DECREE_API_SECRET=$(openssl rand -hex 32)
decree-api -check     # validates the config, the machines and decree; exits 0 or 1
decree-api            # listens on 127.0.0.1:8801 and supervises `decree daemon`
```

From another shell (with the same `DECREE_API_SECRET`):

```sh
curl -sS -X POST http://127.0.0.1:8801/notify/backup \
  -H "Authorization: Bearer $DECREE_API_SECRET" \
  --data-binary 'The nightly backup finished.'
# {"id":"20261005T073848Z-734a8f","path":".decree/inbox/20261005T073848Z-734a8f.md","machine":"notify"}

curl -sS http://127.0.0.1:8801/runs/20261005T073848Z-734a8f \
  -H "Authorization: Bearer $DECREE_API_SECRET"
# decree status <id> --format json: "status": "finished", "state": "done", and the run's events
```

Every key of the config, with its default, is in [SPEC.md §3](SPEC.md#3-configuration-decree-apiyml) and in [`decree-api.schema.json`](decree-api.schema.json); [`decree-api.example.yml`](decree-api.example.yml) uses most of them. The config is reloaded when it changes, without restarting the daemon ([§8](SPEC.md#8-reloading-the-config)).

## Endpoints

Every configured endpoint is a `POST` with `Authorization: Bearer <secret>`. Its body is opaque text, given to `decree emit` on stdin; it answers 201 with `{"id", "path", "machine"}`, 400 when decree rejects the message (with decree's message), and 401, 404, 405, 413 or 429 as [SPEC.md §4](SPEC.md#4-handling-a-request) describes. Every error is JSON, `{"error": "…"}`.

The built-ins ([§7](SPEC.md#7-built-in-endpoints)), each switched off under `builtins:` except `/healthz`:

| Endpoint | Auth | Does |
| --- | --- | --- |
| `GET /healthz` | none | 200 `{"ok": true, "daemon": {"enabled", "running", "pid", "restarts", "since"}, "config": {"loaded_at", "error": null}}`. 503 with the same body when the daemon is enabled but not running, or the last config reload failed. Exempt from rate limits. `decree-api -healthcheck` requests it, for container health checks with no curl. |
| `GET /runs/{id}` | default secret | `decree status <id> --format json`: 200 with decree's document unchanged; 404 for an unknown id. |
| `POST /runs/{wait_id}/replies/{event}` | default secret | `decree event <wait_id> <event> [-m=<body>] --format json`: 201 with decree's `{id, path}`. The body is the optional note. 409 when the run is not waiting, or does not accept the event. |
| `GET /openapi.json` | none | An OpenAPI 3.1 document of every configured endpoint and enabled built-in, generated from the loaded config. |

## Security

decree's [SECURITY.md](https://github.com/jtmckay/decree/blob/main/SECURITY.md) applies in full. A message is an instruction to the machines it names, and decree's built-in machines run Claude with your permissions, so **an endpoint secret is as powerful as shell access to whatever those machines do** ([SPEC.md §10](SPEC.md#10-security)).

- **Secrets** come only from the environment and are at least 32 characters. Generate one with `openssl rand -hex 32`. Give an endpoint that reaches a powerful machine its own `secret_env`.
- **Listen on localhost** (the default). Expose the service only through a TLS reverse proxy, such as Caddy; decree-api does no TLS.
- **Path parameters reach decree only as `--param` values in argv:** no shell, typed by decree, and restricted by their pattern (rejected, never repaired). Bodies reach decree only on stdin.
- **No endpoint can choose the machine:** `message.machine` is fixed in the config, and a placeholder is not allowed there.
- **Rate budgets:** authentication comes before either budget, so unauthenticated traffic can exhaust only the failure budget and never locks out a caller holding the right secret.
- Messages may carry secrets, so decree-api runs with umask `027`, and it never passes its own `DECREE_*` variables on to decree.

## Running it as a service

[`deploy/decree-api.service`](deploy/decree-api.service) is a systemd user unit. Put the secrets in `~/.config/decree-api/env` (mode `600`), adjust the paths in the unit, then:

```sh
cp deploy/decree-api.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now decree-api
loginctl enable-linger "$USER"   # keep it running when you are logged out
journalctl --user -u decree-api -f
```

There is no container image: the daemon runs the project's scripts, so its environment needs every tool they use. A container is your choice, built on an image that has those tools.

## Development

```sh
gofmt -l . && go vet ./... && go test -race ./...
```

Handler tests use a stub `decree`; the end-to-end test (`TestEndToEnd`) uses the real one, and is skipped with a note when `decree` is not on `PATH`.

This repository is built by decree itself: the migrations in `.decree/migrations/` implement SPEC.md one section at a time, with the `go_develop` machine. `decree process` runs the next ones; `decree status` shows what ran.
