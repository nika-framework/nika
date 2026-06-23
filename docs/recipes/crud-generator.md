# CRUD Generator

> **Coming Soon** — A CRUD generator is not yet implemented.

The CRUD generator will automatically create full Create, Read, Update, Delete operations for your models.

## Planned Design

```bash
nika generate crud users --model User
```

This would generate:
- `user_controller.go` — Controller with CRUD routes
- `user_service.go` — Service with business logic
- `user_repository.go` — Repository for data access
- `user_dto.go` — DTOs for validation

## Current Alternative

Follow the patterns described in [Controllers](../overview/controllers.md), [Providers](../overview/providers.md), and [Mongo](../techniques/mongodb.md) to manually create CRUD operations.

## Status

| Feature | Status |
|---------|--------|
| CRUD scaffolding | ⏳ Planned |
| Template customization | ⏳ Planned |
| Interactive generation | ⏳ Planned |

!!! info "Want to contribute?"
    The CRUD generator is open for contribution.
