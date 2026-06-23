# Prisma

> **Coming Soon** — Prisma integration is not yet implemented.

[Prisma](https://www.prisma.io/) is a next-generation ORM for Node.js and TypeScript. A Go Prisma client is available at [prisma/prisma-client-go](https://github.com/prisma/prisma-client-go).

## Current Alternative

Use Prisma Go Client directly:

```bash
go install github.com/steebchen/prisma-client-go@latest
```

```go
import "github.com/your-project/prisma-client-go"

func NewUserService(client *prisma.Client) *UserService {
    return &UserService{client: client}
}

func (s *UserService) FindAll() ([]User, error) {
    return s.client.User.FindMany().Exec(context.Background())
}
```

## Status

| Feature | Status |
|---------|--------|
| Prisma Go integration | ⏳ Planned |
| Schema generation | ⏳ Planned |
| Migration support | ⏳ Planned |
