---
machine: develop
---
# 10: Read-only endpoints over a machine's state

## Overview

A decree machine keeps what others may read in its `state/` folder. For example, `decree_lease` writes `state/status/leases/<id>.json` and `state/status/resources/<resource>.json`, and `decree_registry` writes `state/health.json`. A machine or a service on another host that wants to know whether its lease is held, or where a service is, has to read those files. Today that needs shell access to the project.

Add a third endpoint action, `read`, that serves files from one machine's `state/` folder over HTTP, read-only, following REST conventions. It never runs decree and never writes. Nothing outside `.decree/machines/<machine>/state/` can be reached through it.

Prior art:

- Static file servers, such as `http.FileServer` and nginx `autoindex`: `GET` returns a file, and a directory returns a listing.
- HTTP conditional requests (RFC 9110, sections 8.8 and 13): `ETag`, `Last-Modified`, `If-None-Match`, and `304`.
- HTTP range requests (RFC 9110, section 14).
- `href` links in each listing entry (HAL, JSON:API `links`).

## Configuration

```yaml
endpoints:
  # A directory: GET /leases lists it; GET /leases/<file> returns one file in it, at any
  # depth below (GET /leases/sub/x.json).
  - path: /leases
    action: read
    secret_env: DECREE_GO_REST_READ_SECRET
    read:
      machine: gpu_lease
      path: status/leases # under .decree/machines/gpu_lease/state/

  # One file per request, named by a path parameter.
  - path: /leases/by-id/{id}
    action: read
    patterns: { id: "[0-9TZ.-]+-[a-f0-9]{6}" }
    read:
      machine: gpu_lease
      path: status/leases/{{id}}.json

  # The whole state folder of a machine.
  - path: /registry
    action: read
    read: { machine: decree_registry }
```

- **`read.machine`** is required: a machine installed in the project, meaning `.decree/machines/<machine>/machine.yml` exists, checked at validation.
- **`read.path`** is optional, and defaults to the whole `state/` folder.
  - It is relative and made of segments matching `[A-Za-z0-9._-]+`. No segment is `.` or `..`, and none starts with `.`. It may hold `{{name}}` placeholders for path parameters.
  - Every path parameter is used in it, as for the other actions.
  - It need not exist at startup, because state is written over time.
- **A `read` endpoint** forbids `message`, `reply` and `body`, and takes `secret_env` and `patterns` as the other actions do.

## Handling a request

The routing, authentication, budget and parameter steps of SPEC §4 apply unchanged. A `read` endpoint answers `GET` and `HEAD`. Any other method is a 405 with `Allow: GET, HEAD`, charged to the failure budget.

**What is served.** It is decided per request:

- **No path parameters, and `read.path` is a directory.**
  - The endpoint's own path (`/leases`, `/leases/`) returns the listing.
  - `<endpoint path>/<rest>` returns the file or directory at `<rest>` below it, at any depth.
  - Each segment of `<rest>` must match `[A-Za-z0-9._-]+` and must not start with `.`. Otherwise it is a 400.
- **`read.path` is a file.** The endpoint's path returns it, and nothing below it is served.
- **`read.path` has placeholders.** The substituted path is served, whether it is a file or a directory. There is no `<rest>`.
- **Nothing there:** a 404. This spends from the authenticated budget, not the failure budget, because the route matched.

**Containment.**

- The resolved path, after symlinks (`filepath.EvalSymlinks`), must be inside the machine's resolved `state/` folder. Otherwise it is a 404, the same as missing, so a symlink cannot reveal anything outside.
- Only regular files and directories are served. Anything else is a 404.
- Names starting with `.` are never served and never listed.

**A file.**

- Served with `http.ServeContent`, which handles `HEAD`, `Range`, `If-Modified-Since` and `If-None-Match`.
- `Last-Modified` is the file's modification time. `ETag` is a strong tag of its content's SHA-256.
- `Cache-Control: no-cache`, so a client revalidates each time and gets a `304` while nothing changed.
- **`Content-Type` by extension:**
  - `.json`: `application/json`
  - `.md`: `text/markdown; charset=utf-8`
  - `.txt`, `.log` and `.yml`/`.yaml`: `text/plain; charset=utf-8`
  - anything else: `application/octet-stream`
- `X-Content-Type-Options: nosniff`.

**A directory.**

- `200`, `application/json`:

  ```json
  {
    "path": "/leases",
    "items": [
      {"name": "x.json", "type": "file", "size": 412, "modified": "2026-11-24T17:00:00Z", "href": "/leases/x.json"},
      {"name": "old", "type": "directory", "modified": "...", "href": "/leases/old/"}
    ]
  }
  ```

- Sorted by name. `href` is the URL path to request the entry. A directory's `href` ends in `/`.
- It has an `ETag` of the listing's bytes, and answers `If-None-Match` with `304`.
- A listing holds at most 1000 entries. Beyond that it says `"truncated": true`.

## Everything else

- **Validation** (SPEC §3) gains the `read` rules above: shape, placeholders, and the machine exists.
- **OpenAPI** documents each `read` endpoint as `GET` and `HEAD`, with:
  - its path parameters;
  - for a directory, the `{rest}` form;
  - the listing schema;
  - file responses as `application/octet-stream` with the content types above;
  - `304`, `404` and the bearer scheme.
- **Logging** follows §9: the route pattern, never the path asked for or a file name.
- **Security** (SPEC §10):
  - `state/` can hold anything a machine writes, so give `read` endpoints their own `secret_env`.
  - A `read` endpoint can reach only the one machine's `state/`, never `runs/`, `inbox/`, `scripts/` or `.env`.
  - The container needs no change: `.decree/` is already mounted read-only.
- **The example config** gains a `read` endpoint for a lease's status. The README says how a machine checks a lease over HTTP:

  ```bash
  curl -H "Authorization: Bearer …" https://…/leases/by-id/<id>
  ```

- **SPEC.md** gains this as its own section, with the request steps in §4, and §13 gains a row.

## Constraints

- No new dependency. Standard library and `gopkg.in/yaml.v3` only.
- `emit` and `reply` endpoints behave exactly as before.
- Tests use `httptest` and temp directories. They cover:
  - the listing;
  - a file, with each content type;
  - `HEAD`, `Range`, `If-None-Match` with `304`, and `405` with `Allow`;
  - a 404 for a missing file, a dotfile, a symlink out of `state/`, and a FIFO;
  - a 400 for a bad segment, `..` and an encoded `%2e%2e`;
  - placeholders;
  - truncation at 1000 entries;
  - the auth and budget order;
  - validation errors;
  - the OpenAPI document (golden).
- If SPEC.md and this order disagree in a way you cannot settle, write the question to `STOP` in the run directory and end without further changes.
- The gate passes: `gofmt -l .` lists nothing, and `go vet ./...` and `go test -race ./...` pass.
- Print the evidence for each acceptance criterion at the end of your reply.

## Acceptance Criteria

- **Given** a `read` endpoint `/leases` over `status/leases` holding `a.json`, `.hidden` and `old/b.json`
  **When** `GET /leases` and `GET /leases/old/` are made with the secret
  **Then** the first lists `a.json` and `old/` (not `.hidden`) with their `href`s, and the second lists `b.json`

- **Given** that endpoint
  **When** `GET /leases/a.json` is made, then again with its `ETag` in `If-None-Match`
  **Then** the first is 200 `application/json` with the file's bytes, `ETag` and `Last-Modified`, and the second is 304 with no body

- **Given** a symlink in `state/status/leases/` that points outside `state/`
  **When** it is requested, or `/leases/..%2f..%2fscripts` is
  **Then** the answer is 404 or 400, and nothing outside `state/` is read

- **Given** `/leases/by-id/{id}` over `status/leases/{{id}}.json`
  **When** an id that matches the pattern, one that does not, and one with no file are requested
  **Then** the answers are 200, 400 and 404

- **Given** a `read` endpoint
  **When** it receives a `POST`, or a `GET` without the secret
  **Then** the answers are 405 with `Allow: GET, HEAD`, and 401, both charged to the failure budget

- **Given** a `read` endpoint naming a machine the project does not have, a `read.path` with `..`, or a `body` key
  **When** `decree-go-rest -check` runs
  **Then** it exits 1 and names each error
