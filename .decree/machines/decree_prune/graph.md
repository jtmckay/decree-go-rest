# decree_prune

Each week, delete the folders of finished runs older than PRUNE_OLDER_THAN (default 30d).

Machine: [machines/decree_prune/machine.yml](machine.yml)

```mermaid
stateDiagram-v2
    [*] --> prune
    prune --> done: done
    prune --> failed: error (implicit)
    done --> [*]
    failed --> [*]
```
