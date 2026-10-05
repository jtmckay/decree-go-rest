# All machines

Every machine, the `emits` and `invokes` edges between them, and cron entry points.

- [develop](develop.md): Implement a message with Claude, then have it verify the acceptance criteria.
- [go_develop](go_develop.md): Implement a message in a Go project with Claude in small, logged steps, run the gate (gofmt, go vet, go test), and have Claude fix failures only if the gate fails.
- [router](router.md): Ask Claude to pick one of the options in the request.

```mermaid
flowchart LR
    develop["develop"]
    go_develop["go_develop"]
    router["router"]
```
