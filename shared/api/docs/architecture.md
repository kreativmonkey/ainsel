# Ainsel API Shared Architecture

## Overview

The `ainsel-api-shared` module is the shared foundation for all Ainsel platform components. It provides the canonical event schema and the filter engine, plus small naming helpers shared between the hub and the agent operator.

## Module Structure

```mermaid
graph TD
    subgraph ainsel-api-shared
        ES[Event Schema<br/>event.go]
        FE[Filter Engine<br/>filter.go]
    end

    ES -->|Event struct used by| FE
```

## Event Schema

The canonical `Event` struct is the normalized format that all connectors produce and ainsel-hub-backend consumes. Every event flowing through the platform conforms to this schema.

```mermaid
classDiagram
    class Event {
        +string ID
        +string Version
        +string Source
        +string Connector
        +string Type
        +time.Time Timestamp
        +EventSubject Subject
        +EventActor Actor
        +string Action
        +RawJSON Data
        +string Raw
    }

    class EventSubject {
        +string Kind
        +string Owner
        +string Repo
        +int Number
    }

    class EventActor {
        +string Login
        +string DisplayName
        +bool IsBot
    }

    Event --> EventSubject
    Event --> EventActor
```

## Filter Engine

The filter engine evaluates conditions against the event `Data` payload using dotted field paths and a set of operators. Multiple filters are combined with AND logic.

```mermaid
flowchart TD
    A[Incoming Event] --> B[Extract Data payload]
    B --> C{For each Filter}
    C --> D[Resolve dotted field path]
    D --> E{Field exists?}
    E -->|No| F[Return false]
    E -->|Yes| G{Apply operator}
    G -->|Match| H{More filters?}
    G -->|No match| F
    H -->|Yes| C
    H -->|No| I[Return true - all matched]
```

### Field Resolution

Fields use dotted notation to navigate nested JSON:
- `issue.title` resolves to `data.issue.title`
- `comment.body` resolves to `data.comment.body`

### Event Type Matching

The `MatchEventType` function supports wildcard patterns:
- `*` matches all event types
- `issue.*` matches `issue.opened`, `issue.comment.created`, etc.
- `push` matches only `push`

Event type strings live in the canonical event's `type` header (connectors
stamp it from the incoming webhook payload); see
[`docs/event-schema.md`](../../../docs/event-schema.md).
