# `nika generate`

Generate Nika resource code from templates.

```bash
nika generate <type> <module>
nika g <type> <module>
```

The command must run from a project root containing `go.mod`. Module names are
lowercase and may contain letters, digits, and underscores; they must begin
with a lowercase letter.

Templates are compiled into the binary, so generation works from an installed
release, not only from a checkout of this repository.

## Generation types

| Type | Alias | Generated files |
|------|-------|-----------------|
| `res` | `r` | Full resource: schema, DTOs, services, controllers, responses, module |
| `controller` | `c` | Controller base and CRUD handlers |
| `service` | `s` | Service base and CRUD methods |
| `dto` | `d` | Create, update, find-one, and list DTOs |
| `response` | `rs` | Response models and mapper |
| `migration` | `m` | A database migration (see below) |
| `seed` | — | A database seed |

Examples:

```bash
nika generate res user
nika g r product
nika generate controller order
nika generate service order
nika generate dto order
nika generate response order
```

## Flags

| Flag | Purpose |
|------|---------|
| `-d`, `--database` | `mongodb`, `postgres`, `mysql`, or `sqlite`; skips the prompt |
| `-a`, `--app` | Which microservice to generate into; skips the prompt |
| `-f`, `--format` | Migration format: `go` (default) or `sql` |
| `-m`, `--model` | Path to a Go model file, to derive real DDL or seed data |
| `--type` | Struct name inside `--model`, when the file has several |
| `--table` | Override the derived table or collection name |

## Choosing the target app

In a microservice workspace the command asks which service the module belongs
to, since generating into the wrong process produces code nothing loads:

```bash
nika g res user
```

```text
  Which microservice should this belong to?
  [1] api  (apps/api/src)
  [2] micro-grpc  (apps/micro-grpc/src)
  Enter number:
```

Answer up front with `-a`, which is what scripts and CI should do:

```bash
nika g res user -a api
nika g res order --app micro-grpc
```

Single-application projects skip this entirely. See [Workspaces](monorepo.md).

## Full resource generation

`res` asks for the database when `-d` is not given, then collects fields
interactively. For each field it asks:

- Field name in snake case.
- Type. MongoDB: `string`, `int`, `int64`, `float64`, `bool`, `time.Time`,
  `primitive.ObjectID`, `[]string`, `map[string]any`. SQL backends:
  `string`, `int`, `int64`, `float64`, `bool`, `time.Time`.
- Whether the field is required.

Finish field input by entering `done` or an empty field name.

The generated resource lands under the target app's source folder:

```text
src/<module>/                    # single application
apps/<service>/src/<module>/     # microservice workspace
├── <module>.module.go
├── schema/
├── dto/
├── services/
├── controllers/
├── response/
└── migrations/                  # SQL backends only
```

Import paths follow that same prefix, for example
`nikaapp/apps/micro-grpc/src/product/dto`.

## Module registration

The generator registers the new module in the app's `app.module.go` — the one
belonging to the target service, not a shared root file:

```text
  ✔ Registered ProductModule in apps/micro-grpc/src/app.module.go
```

It adds the import beside the project's other imports and appends the
constructor to `Imports()`. The edit is made through the Go parser: a file that
does not already parse is left alone with a printed instruction, an edit that
would produce invalid Go is discarded, and a package name that clashes with an
existing import gets an alias — the case where a gateway imports `user-grpc`,
`user-tcp`, and `user-redis`, all package `user`.

Running the same command twice does not duplicate the import or the entry.

## Migrations and seeds

```bash
nika g migration create_users -d postgres
nika g migration add_index_orders -d mongodb --format go
nika g seed initial_admins -d postgres
```

Generate real DDL or seed rows from an existing model instead of a stub:

```bash
nika g migration create_users -d sqlite -m src/user/schema/user.model.go
nika g migration create_users -d sqlite -m src/user/schema/user.model.go --format sql
nika g seed initial_users -d sqlite -m src/user/schema/user.model.go
```

Migrations and seeds are written under `internal/database/`, and the runner at
`cmd/migrate` or `cmd/seed` is scaffolded on first use so `nika migrate` and
`nika seed` work immediately.

## AI-assisted generation

After configuring an agent, a natural-language request can replace the
interactive field collection:

```bash
nika agent init ollama
nika agent "Create a news module with title, text, image, and tags"
```

The agent calls the same generator through its `nika_generate` tool, so the
output is identical to running `nika g res` by hand — including app selection
and module registration. Field types are validated before any file is written.
See [`nika agent`](agent.md).
