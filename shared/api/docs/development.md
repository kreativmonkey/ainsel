# Development Guide

## Prerequisites

- Go 1.22+

## Building

This is a library module with no binary output. To verify it compiles:

```bash
go build ./...
```

## Testing

```bash
# Run all tests
go test ./...

# Run with verbose output
go test -v ./...

# Run with coverage
go test -cover ./...
```

## Key Files

| File | Purpose |
|------|---------|
| `event.go` | Canonical `Event` struct, direct-source (`cron`, `chat`) constants |
| `filter.go` | `Filter` struct, `Match`, `MatchFilters`, `MatchEventType` |
| `filter_deepcopy.go` | DeepCopy methods for CRD compatibility |
| `api/v1alpha1/` | CRD types (`Agent`, `AgentImage`, `WebhookConnector`) |
| `cron/` | Shared cron schedule parsing used by hub and console |
| `personas/`, `skills/` | ConfigMap naming formats shared by hub and agent operator |

## Adding a New Event Type

1. Event types are strings stamped into the event's `type` header by the
   connector — there is no central registry, and event `data` is carried as
   raw JSON (`RawJSON`), so no Go type is needed for a new payload shape
2. Document the payload shape in [`docs/event-schema.md`](../../../docs/event-schema.md)
3. Add tests in `event_test.go`

## Adding a New Filter Operator

1. Add the case to the `Match` method's switch statement in `filter.go`
2. Add test cases in `filter_test.go`
3. Document the operator in `docs/event-schema.md`

## Contributing

- Follow standard Go conventions
- All exported types and functions must have doc comments
- All new functionality must have tests
