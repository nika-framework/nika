# Workspaces (Microservices)

A Nika project has one of two layouts, and the CLI detects which one from disk.
Every command that touches a module — `generate`, `agent`, `start` — follows the
detected layout.

| Layout | Modules live in | Entry point | Import prefix |
|--------|-----------------|-------------|---------------|
| Single application | `src/<module>/` | `main.go` | `<module path>/src` |
| Microservice workspace | `apps/<service>/src/<module>/` | `apps/<service>/main.go` | `<module path>/apps/<service>/src` |

## Workspace layout

A workspace is a directory named `apps/` whose subdirectories are each a
separate process with their own `src/` and `main.go`:

```text
my-app/
├── go.mod
├── .nika.toml
└── apps/
    ├── api/                    ← the HTTP gateway
    │   ├── main.go
    │   └── src/
    │       ├── app.module.go
    │       └── user-grpc/
    ├── micro-grpc/
    │   ├── main.go
    │   └── src/
    │       ├── app.module.go
    │       └── user/
    └── micro-tcp/
        ├── main.go
        └── src/
```

A directory under `apps/` counts as a service when it contains a `main.go` or a
`src/` folder. Anything else — a `proto/` or `docs/` folder sitting alongside
them — is ignored.

## Detection

No configuration is required. The CLI looks for `apps/` first, then falls back
to `src/` and `main.go`:

```bash
nika app list
```

```text
Microservice workspace — module nikaapp

 * api              src: apps/api/src             run: go run ./apps/api/main.go
                    modules: [user-grpc user-redis user-tcp]
   micro-grpc       src: apps/micro-grpc/src      run: go run ./apps/micro-grpc/main.go
                    modules: [user]
   micro-tcp        src: apps/micro-tcp/src       run: go run ./apps/micro-tcp/main.go
                    modules: [user]

 * = default for `nika start` (change it with `nika app use <name>`)
```

To pin the layout and stop detection — for example when `apps/` exists for an
unrelated reason — set the mode explicitly:

```toml
[workspace]
  mode = "single"
```

## Generating into a service

In a workspace, `nika generate` asks which service the module belongs to,
because there is no correct guess and picking wrong writes a whole module into
a process that never loads it:

```bash
nika g res user
```

```text
  Which microservice should this belong to?
  [1] api  (apps/api/src)
  [2] micro-grpc  (apps/micro-grpc/src)
  [3] micro-tcp  (apps/micro-tcp/src)
  Enter number:
```

Answer up front with `-a` / `--app` to skip the prompt, which is what scripts
and CI should do:

```bash
nika g res user -a api
nika g res order --app micro-grpc
```

Partial names are accepted when they are unambiguous, so `-a grpc` resolves to
`micro-grpc`. A prefix matching two services (`-a micro`) is rejected rather
than guessed.

`NIKA_APP` sets the target for a whole shell session:

```bash
export NIKA_APP=micro-grpc
nika g res order          # no prompt
```

Generated import paths follow the target service:

```go
// apps/micro-grpc/src/product/product.module.go
import (
    "nikaapp/apps/micro-grpc/src/product/controllers"
    "nikaapp/apps/micro-grpc/src/product/schema"
    "nikaapp/apps/micro-grpc/src/product/services"
)
```

The module is registered in that service's own
`apps/<service>/src/app.module.go`, not in a shared root one.

## Running a service

`nika start` runs the default app, or the one named with `-a`:

```bash
nika start                  # the default app
nika start -a micro-grpc    # one specific service
nika start -a api --watch
```

Set the default once:

```bash
nika app use api
```

See [`nika app`](app.md) for the full command reference and
[Configuration](configuration.md) for the `[workspace]` and `[apps.*]` sections
these commands write.

## Shared libraries

Code shared between services is ordinary Go: put it in a package outside
`apps/` — `internal/common/`, for example — and import it by module path. There
is no separate library concept to configure. See [Libraries](libraries.md).
