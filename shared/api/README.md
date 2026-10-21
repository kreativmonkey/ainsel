# Shared API

Go module shared by every Go component in the platform. Provides:

- The canonical `Event` struct and event-type strings. Event `data` is raw
  JSON (`RawJSON`) — payload shapes are documented in the event schema, not
  modelled as Go types.
- The `Filter` struct and `MatchFilters` evaluation engine used by the
  hub's trigger matching.
- Naming helpers for ConfigMaps shared between the hub and the agent
  operator (`personas/`, `skills/`) and the shared cron schedule parser
  (`cron/`).

## Role in the platform

See [`docs/architecture.md`](../../docs/architecture.md).

Pure library — no binary, no runtime, no main package. Imported by
[`services/hub/`](../../services/hub/),
[`services/webhook-receiver/`](../../services/webhook-receiver/),
[`operators/agent/`](../../operators/agent/),
[`operators/event-gateway/`](../../operators/event-gateway/), and the
agent runtime.

## Importing from another package in the workspace

Inside the monorepo, `go.work` joins this module automatically — no
`replace` directive needed. Just import:

```go
import api "github.com/ainsel-project/ainsel/shared/api"
```

Outside the monorepo, vendor the source or pin a version tag.

## Testing

```bash
go test ./...
```

## Reference

- [Event schema](../../docs/event-schema.md)
