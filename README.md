# decree-go-rest

decree-go-rest is the HTTP front door of a [decree](https://github.com/jtmckay/decree) 0.6 project. Endpoints defined in one YAML file turn authenticated `POST`s into decree inbox messages, always through `decree emit`, so ids, validation of the machine and its typed `params`, and trace context stay decree's. An endpoint can instead reply to a run waiting in a `reply` state, through `decree reply`, such as an approval with its own secret, or serve one machine's `state/` folder over `GET`, read-only, such as a lease's status. It runs no work itself: `decree daemon` processes those messages, in a container of its own beside it, or supervised by decree-go-rest when it runs outside Docker. The design, and the source of truth for every behaviour below, is [SPEC.md](SPEC.md).

## Install

decree-go-rest ships as a container image with the `decree` binary it runs:

```sh
docker pull ghcr.io/jtmckay/decree-go-rest:latest
```

Tags: `latest` and `X.Y.Z` / `X.Y` for releases, `main` for the main branch. `linux/amd64` only for now. Running the binary outside Docker is covered under [From source](#from-source).

## Quick start

In a [decree](https://github.com/jtmckay/decree) project with a machine `notify` (setting one up is covered there), a config in `.decree/decree-go-rest.yml`:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/jtmckay/decree-go-rest/main/decree-go-rest.schema.json
endpoints:
  - path: /notify/{title}
    message:
      machine: notify
      params: { title: '{{title}}', priority: high }
```

From the project directory, a secret, a check, then the service. It sees `.decree/` read-only, except `inbox/`, where it queues messages:

```sh
export DECREE_GO_REST_SECRET=$(openssl rand -hex 32)
IMAGE=ghcr.io/jtmckay/decree-go-rest:latest
MOUNTS="-v $PWD/.decree:/project/.decree:ro -v $PWD/.decree/inbox:/project/.decree/inbox"

# validates the config, the machines and decree; exits 0 or 1
docker run --rm --user "$(id -u):$(id -g)" $MOUNTS -e DECREE_GO_REST_SECRET "$IMAGE" -check

# listens on 8801
docker run -d --name decree-go-rest --restart unless-stopped --stop-timeout 15 \
  --user "$(id -u):$(id -g)" $MOUNTS -e DECREE_GO_REST_SECRET \
  -p 127.0.0.1:8801:8801 "$IMAGE"
```

and `decree install && decree daemon` in the project to process what it queues (decree 0.6 installs a project's plugins before the daemon runs), on the host or in a container ([below](#running-it-in-docker)).

Then:

```sh
curl -sS -X POST http://127.0.0.1:8801/notify/backup \
  -H "Authorization: Bearer $DECREE_GO_REST_SECRET" \
  --data-binary 'The nightly backup finished.'
# {"id":"20261005T073848Z-734a8f","path":".decree/inbox/20261005T073848Z-734a8f.md","machine":"notify"}
```

Every key of the config, with its default, is in [SPEC.md §3](SPEC.md#3-configuration-decree-go-restyml) and in [`decree-go-rest.schema.json`](decree-go-rest.schema.json); [`example/.decree/decree-go-rest.yml`](example/.decree/decree-go-rest.yml) uses most of them. The config is reloaded when it changes, without restarting the daemon ([§8](SPEC.md#8-reloading-the-config)).

## Endpoints

Every configured endpoint takes `Authorization: Bearer <secret>`, and has an `action` ([SPEC.md §3](SPEC.md#3-configuration-decree-go-restyml)). `emit` and `reply` take a `POST`; `read` takes `GET` and `HEAD`:

- **`emit`** (the default) queues a new message. The body is opaque text, given to `decree emit` on stdin after an empty frontmatter block, so a body that starts with `---` is still body and only the config sets the message's frontmatter; it answers 201 with `{"id", "path", "machine"}`, and 400 when decree rejects the message (with decree's message).
- **`reply`** replies to a run waiting in a `reply` state, with `decree reply <to> <event> [-m=<body>] --format json`; `<to>` is a wait id, or a run id for its current wait. The body is the optional note, passed unchanged as one argv entry. It answers 201 with `{"id", "path", "to", "event"}`, and 409 when the run is not waiting, or does not accept the event.

```yaml
  # Answering a run's question, such as an approval, deserves its own secret.
  - path: /approve/{wait_id}
    action: reply
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

- **`read`** serves files from one machine's `state/` folder, read-only: it never runs decree and never writes, and nothing outside `.decree/machines/<machine>/state/` can be reached, nor any name starting with `.` ([SPEC.md §14](SPEC.md#14-reading-a-machines-state)). A file comes with a strong `ETag` (its SHA-256), `Last-Modified` and `Cache-Control: no-cache`, so a client that sends `If-None-Match` gets a `304` while nothing changed; `Range` works too. A directory is a JSON listing, `{"path", "items": [{"name", "type", "size", "modified", "href"}]}`, sorted by name, at most 1000 entries. Without path parameters, `GET <path>/<rest>` serves what lies below `read.path`, at any depth.

**Checking a lease over HTTP.** [`decree_lease`](https://github.com/jtmckay/decree) writes each lease's status to `state/status/leases/<id>.json`. The example config serves it, on its own secret:

```yaml
  - path: /leases/by-id/{id}
    action: read                    # GET and HEAD; never runs decree
    secret_env: DECREE_GO_REST_READ_SECRET
    patterns: { id: '[0-9TZ.-]+-[a-f0-9]{6}' }
    read:
      machine: gpu_lease
      path: 'status/leases/{{id}}.json'  # under .decree/machines/gpu_lease/state/
```

so a machine, or a service on another host, checks whether its lease is held with:

```sh
curl -H "Authorization: Bearer $DECREE_GO_REST_READ_SECRET" https://decree.example.com/leases/by-id/<id>
# 200 with the lease's JSON; 404 while there is none
```

A script that polls can keep the `ETag` and send it back as `If-None-Match`, to get a `304` until the status changes.

Each answers 401, 404, 405, 413 (not `read`) or 429 as [SPEC.md §4](SPEC.md#4-handling-a-request) describes, and 500 when decree, or reading the state, fails otherwise. Every error is JSON, `{"error": "…"}`.

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
| `DECREE_GO_REST_LISTEN` | `0.0.0.0:8801` in the image | Overrides `listen` when set. |
| `DECREE_GO_REST_OPENAPI` | `false` | `true` serves `GET /openapi.json`; `false` does not. Any other value stops decree-go-rest from starting. Read at startup. |

## Security

decree's [SECURITY.md](https://github.com/jtmckay/decree/blob/main/SECURITY.md) applies in full. A message is an instruction to the machines it names, and decree's built-in machines run Claude with your permissions, so **an endpoint secret is as powerful as shell access to whatever those machines do** ([SPEC.md §10](SPEC.md#10-security)).

- **Secrets** come only from the environment and are at least 32 characters. Generate one with `openssl rand -hex 32`. Give an endpoint that reaches a powerful machine its own `secret_env`.
- **Approvals get their own secret.** Answering a run's question, such as an approval, deserves its own secret: give each `action: reply` endpoint its own `secret_env`, as the example config's `/approve/{wait_id}` does, so a caller that may queue messages cannot also approve them.
- **Listen on localhost:** publish the container's port on `127.0.0.1` (as above), or keep it on a Compose network, and expose the service only through a TLS reverse proxy, such as Caddy; decree-go-rest does no TLS.
- **Path parameters reach decree only as argv entries,** `--param` values or `decree reply`'s `<to>` and `<event>`: no shell, typed by decree, and restricted by their pattern (rejected, never repaired). Bodies reach decree only on stdin; a note only as the one argv entry `-m=<body>`.
- **No endpoint can choose the machine:** `message.machine` and `read.machine` are fixed in the config, and a placeholder is not allowed there.
- **Read endpoints get their own secret,** since a machine's `state/` can hold anything it writes, as the example's `/leases/by-id/{id}` does with `DECREE_GO_REST_READ_SECRET`. A `read` endpoint reaches only its machine's `state/` folder, after symlinks, never `runs/`, `inbox/`, `scripts/` or `.env`; anything else is a 404.
- **Rate budgets:** authentication comes before either budget, so unauthenticated traffic can exhaust only the failure budget and never locks out a caller holding the right secret.
- Messages may carry secrets, so decree-go-rest runs with umask `027`, and it never passes its own `DECREE_*` variables on to decree.

## Running it in Docker

The image runs `decree-go-rest -config /project/.decree/decree-go-rest.yml`; arguments are added after it, as `-check` above. It needs the project's `.decree/` mounted at `/project/.decree`, read-only, with `inbox/` mounted read-write over it: the endpoints write nothing but the messages `decree emit` and `decree reply` queue there, and with the daemon off decree-go-rest writes nothing else. It listens on `0.0.0.0:8801` inside the container, its health check is `-healthcheck`, and it logs JSON lines on stderr. Run it as the owner of the project (`--user`, uid 1000 by default), the same user as the daemon, so the daemon can claim its messages.

**The daemon runs in its own container.** The image sets `DECREE_GO_REST_DAEMON=false`: `decree daemon` runs your machines' scripts, which need their own tools and the project read-write, so it does not belong in the container that takes requests. [`example/compose.yml`](example/compose.yml) runs both:

- **`api`**, this image, with the mounts above (a `read` endpoint needs nothing more: `.decree/` is already mounted read-only), the secrets from a mode `600` `.env`, and the port on `127.0.0.1`;
- **`daemon`**, built from [`example/daemon/Dockerfile`](example/daemon/Dockerfile): Ubuntu with decree `v0.6.0` and nothing your scripts need yet, which is yours to fill in. It runs `decree install && decree daemon`, mounts the whole project read-write and publishes no port.

Copy `compose.yml` and `daemon/` to your project, and [`example/.decree/decree-go-rest.yml`](example/.decree/decree-go-rest.yml) into its `.decree/`.

The config's directory is mounted, not the file, so editing `.decree/decree-go-rest.yml` reloads it ([SPEC.md §8](SPEC.md#8-reloading-the-config)).

### Building the image

`docker build -t decree-go-rest .` builds the same image from a checkout. `DECREE_TAG` picks the decree release whose `install.sh` and binary the image installs (default `v0.6.0`) and `VERSION` sets what `-version` prints. [`.github/workflows/docker.yml`](.github/workflows/docker.yml) tests, then publishes on every push to `main` and every `v*` tag; tagging a release is `git tag v1.2.3 && git push origin v1.2.3`.

### From source

Needs Go 1.22 or newer to build, and `decree` 0.6.0 or newer on `PATH` (a `0.6.0-rc.N` release candidate counts) (or at `decree:` in the config) to run.

```sh
go install github.com/jtmckay/decree-go-rest/cmd/decree-go-rest@latest
decree-go-rest -check && decree-go-rest   # from the project directory; listens on 127.0.0.1:8801
```

## Development

```sh
gofmt -l . && go vet ./... && go test -race ./...
```

Handler tests use a stub `decree`; the end-to-end test (`TestEndToEnd`) uses the real one, and is skipped with a note when `decree` (0.6.0 or newer) is not on `PATH`. It builds its temp project in decree 0.6's layout: `machines/<name>/machine.yml`, scripts in `machines/<name>/scripts/`, and a `reply` state answered through an `action: reply` endpoint.

This repository is built by decree itself: the `develop` machine's order files, in `.decree/machines/develop/order/`, implement SPEC.md one section at a time, with a Go gate (`gofmt`, `go vet`, `go test -race`). `decree daemon` runs them; `decree status` shows what ran.
