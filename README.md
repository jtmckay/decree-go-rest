# decree-go-rest

decree-go-rest is the HTTP front door of a [decree](https://github.com/jtmckay/decree) 0.5 project. Endpoints defined in one YAML file turn authenticated `POST`s into decree inbox messages, always through `decree emit`, so ids, validation of the machine and its typed `params`, and trace context stay decree's. An endpoint can instead reply to a run waiting for a person, through `decree event`, such as an approval with its own secret. It keeps `decree daemon` running, so those messages are processed. It runs no work itself: decree does. The design, and the source of truth for every behaviour below, is [SPEC.md](SPEC.md).

## Install

Needs Go 1.22 or newer to build, and `decree` 0.5 or newer on `PATH` (or at `decree:` in the config) to run.

```sh
go install github.com/jtmckay/decree-go-rest/cmd/decree-go-rest@latest
```

or, from a checkout, a static binary:

```sh
CGO_ENABLED=0 go build -o decree-go-rest ./cmd/decree-go-rest
```

`decree-go-rest -version` prints the version it was built from.

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

A config, `decree-go-rest.yml`, next to `.decree/`:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/jtmckay/decree-go-rest/main/decree-go-rest.schema.json
endpoints:
  - path: /notify/{title}
    message:
      machine: notify
      params: { title: '{{title}}', priority: high }
```

A secret, then run it:

```sh
export DECREE_GO_REST_SECRET=$(openssl rand -hex 32)
decree-go-rest -check     # validates the config, the machines and decree; exits 0 or 1
decree-go-rest            # listens on 127.0.0.1:8801 and supervises `decree daemon`
```

From another shell (with the same `DECREE_GO_REST_SECRET`):

```sh
curl -sS -X POST http://127.0.0.1:8801/notify/backup \
  -H "Authorization: Bearer $DECREE_GO_REST_SECRET" \
  --data-binary 'The nightly backup finished.'
# {"id":"20261005T073848Z-734a8f","path":".decree/inbox/20261005T073848Z-734a8f.md","machine":"notify"}

decree status 20261005T073848Z-734a8f   # in the project: "finished" in state done
```

Every key of the config, with its default, is in [SPEC.md §3](SPEC.md#3-configuration-decree-go-restyml) and in [`decree-go-rest.schema.json`](decree-go-rest.schema.json); [`decree-go-rest.example.yml`](decree-go-rest.example.yml) uses most of them. The config is reloaded when it changes, without restarting the daemon ([§8](SPEC.md#8-reloading-the-config)).

## Endpoints

Every configured endpoint is a `POST` with `Authorization: Bearer <secret>`, and has an `action` ([SPEC.md §3](SPEC.md#3-configuration-decree-go-restyml)):

- **`emit`** (the default) queues a new message. The body is opaque text, given to `decree emit` on stdin; it answers 201 with `{"id", "path", "machine"}`, and 400 when decree rejects the message (with decree's message).
- **`event`** replies to a run waiting in a `person` state, with `decree event <to> <event> [-m=<body>] --format json`. The body is the optional note, passed unchanged as one argv entry. It answers 201 with `{"id", "path", "to", "event"}`, and 409 when the run is not waiting, or does not accept the event.

```yaml
  # Answering a run's question, such as an approval, deserves its own secret.
  - path: /approve/{wait_id}
    action: event
    secret_env: DECREE_GO_REST_APPROVE_SECRET
    patterns: { wait_id: '[A-Za-z0-9._-]{1,128}' }
    body: optional                  # the note
    reply:
      to: '{{wait_id}}'
      event: approve
```

```sh
curl -sS -X POST "http://127.0.0.1:8801/approve/$WAIT_ID" \
  -H "Authorization: Bearer $DECREE_GO_REST_APPROVE_SECRET" \
  --data-binary 'Looks good.'
```

Both answer 401, 404, 405, 413 or 429 as [SPEC.md §4](SPEC.md#4-handling-a-request) describes, and 500 when decree fails otherwise. Every error is JSON, `{"error": "…"}`.

Besides the endpoints ([§7](SPEC.md#7-health-and-openapi)):

| Endpoint | Auth | Does |
| --- | --- | --- |
| `GET /healthz` | none | Always served. 200 `{"ok": true, "daemon": {"enabled", "running", "pid", "restarts", "since"}, "config": {"loaded_at", "error": null}}`. 503 with the same body when the daemon is enabled but not running, or the last config reload failed. Exempt from rate limits. `decree-go-rest -healthcheck` requests it, for container health checks with no curl. |
| `GET /openapi.json` | none | Served only when `DECREE_GO_REST_OPENAPI=true`, since it lists every path; otherwise a 404. An OpenAPI 3.1 document of every endpoint, described by its action, generated from the loaded config. |

Neither path may be used by a configured endpoint.

## Environment

| Variable | Default | Meaning |
| --- | --- | --- |
| `DECREE_GO_REST_SECRET` | | The default bearer secret, at least 32 characters. Its name is the config's `secret_env`. |
| `DECREE_GO_REST_LISTEN` | | Overrides `listen` when set. |
| `DECREE_GO_REST_OPENAPI` | `false` | `true` serves `GET /openapi.json`; `false` does not. Any other value stops decree-go-rest from starting. Read at startup. |

## Security

decree's [SECURITY.md](https://github.com/jtmckay/decree/blob/main/SECURITY.md) applies in full. A message is an instruction to the machines it names, and decree's built-in machines run Claude with your permissions, so **an endpoint secret is as powerful as shell access to whatever those machines do** ([SPEC.md §10](SPEC.md#10-security)).

- **Secrets** come only from the environment and are at least 32 characters. Generate one with `openssl rand -hex 32`. Give an endpoint that reaches a powerful machine its own `secret_env`.
- **Approvals get their own secret.** Answering a run's question, such as an approval, deserves its own secret: give each `action: event` endpoint its own `secret_env`, as the example config's `/approve/{wait_id}` does, so a caller that may queue messages cannot also approve them.
- **Listen on localhost** (the default). Expose the service only through a TLS reverse proxy, such as Caddy; decree-go-rest does no TLS.
- **Path parameters reach decree only as `--param` values in argv:** no shell, typed by decree, and restricted by their pattern (rejected, never repaired). Bodies reach decree only on stdin.
- **No endpoint can choose the machine:** `message.machine` is fixed in the config, and a placeholder is not allowed there.
- **Rate budgets:** authentication comes before either budget, so unauthenticated traffic can exhaust only the failure budget and never locks out a caller holding the right secret.
- Messages may carry secrets, so decree-go-rest runs with umask `027`, and it never passes its own `DECREE_*` variables on to decree.

## Running it as a service

[`deploy/decree-go-rest.service`](deploy/decree-go-rest.service) is a systemd user unit. Put the secrets in `~/.config/decree-go-rest/env` (mode `600`), adjust the paths in the unit, then:

```sh
cp deploy/decree-go-rest.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now decree-go-rest
loginctl enable-linger "$USER"   # keep it running when you are logged out
journalctl --user -u decree-go-rest -f
```

### Container

The [`Dockerfile`](Dockerfile) builds decree-go-rest with the `decree` binary (tag `DECREE_TAG`, default `v0.5.0-beta.2`) on Debian slim. Mount the project at `/project`, with `decree-go-rest.yml` in it. It listens on `0.0.0.0:8801`, runs as uid 1000, and its health check is `-healthcheck`.

```sh
docker build -t decree-go-rest .
docker run -d -p 127.0.0.1:8801:8801 -e DECREE_GO_REST_SECRET -v "$PWD:/project" decree-go-rest
```

The image has bash and CA certificates, not your scripts' tools. Either set `daemon: { enabled: false }` and let another container that has them run `decree daemon` on the same `.decree/`, so this one only queues messages, or build `FROM` this image and install what your scripts need.

## Development

```sh
gofmt -l . && go vet ./... && go test -race ./...
```

Handler tests use a stub `decree`; the end-to-end test (`TestEndToEnd`) uses the real one, and is skipped with a note when `decree` is not on `PATH`.

This repository is built by decree itself: the migrations in `.decree/migrations/` implement SPEC.md one section at a time, with the `develop` machine and a Go gate (`gofmt`, `go vet`, `go test -race`). `decree process` runs the next ones; `decree status` shows what ran.
