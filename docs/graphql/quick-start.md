# GraphQL Quick Start

> **Coming Soon** — GraphQL support is not yet implemented.

Nika plans to support GraphQL as an alternative to REST APIs.

## Planned Integration

```go
// Planned: GraphQL module
type GraphQLModule struct{}

func NewGraphQLModule() *GraphQLModule {
    return &GraphQLModule{}
}

func (m *GraphQLModule) Providers() []interface{} {
    return []interface{}{
        // GraphQL resolvers, schema, etc.
    }
}
```

## Current Alternative

Use [gqlgen](https://gqlgen.dev/) directly with Gin:

```bash
go get github.com/99designs/gqlgen
```

## Status

| Feature | Status |
|---------|--------|
| GraphQL schema support | ⏳ Planned |
| Resolvers | ⏳ Planned |
| Mutations | ⏳ Planned |
| Subscriptions | ⏳ Planned |
| Federation | ⏳ Planned |

!!! info "Want to contribute?"
    GraphQL support is open for contribution.
