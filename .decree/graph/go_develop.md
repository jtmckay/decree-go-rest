# go_develop

Implement a message in a Go project with Claude in small, logged steps, run the gate (gofmt, go vet, go test), and have Claude fix failures only if the gate fails.

Machine: [machines/go_develop.yml](../machines/go_develop.yml)

```mermaid
stateDiagram-v2
    [*] --> precheck
    final_gate --> done: done
    final_gate --> failed: error (implicit)
    gate --> done: done
    gate --> qa: error
    implement --> gate: done
    implement --> failed: error (implicit)
    implement --> failed: stop
    precheck --> implement: done
    precheck --> failed: error (implicit)
    qa --> final_gate: done
    qa --> failed: error (implicit)
    qa --> failed: stop
    done --> [*]
    failed --> [*]
```
