# decree-api

The HTTP front door of a [decree](https://github.com/jtmckay/decree) 0.5 project. Endpoints defined in YAML turn authenticated POSTs into decree inbox messages through `decree emit`, it keeps `decree daemon` running, and built-in endpoints read a run's status and reply to runs waiting for a person. The design is [SPEC.md](SPEC.md).

## Building it with decree

This repository is built by decree itself: the migrations in `.decree/migrations/` implement SPEC.md one section at a time, with the `go_develop` machine (Claude implements in small logged steps, then a gate of `gofmt -l`, `go vet ./...` and `go test -race ./...`, and QA only when the gate fails).

Needs `go` (1.22 or newer), `claude` and `decree` (0.5) on `PATH`.

```sh
decree process          # runs the next migrations, in order, until one fails or all are done
decree status           # what ran, what is waiting
decree process --retry  # after fixing whatever a failed migration reported (a STOP file asks a question)
```

Review and commit after each migration; `migration 06` replaces this README with the user-facing one.
