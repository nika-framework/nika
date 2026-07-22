# `nika swagger`

Manage Swagger/OpenAPI documentation using `swaggo/swag`.

```bash
nika swagger init [flags]
nika swagger fmt [flags]
```

Before running a Swagger subcommand, Nika checks whether `swag` is installed.
If it is missing, the CLI installs it with:

```bash
go install github.com/swaggo/swag/cmd/swag@latest
```

## Initialize documentation

```bash
nika swagger init
```

Available flags:

| Flag | Default | Purpose |
|------|---------|---------|
| `--dir` | `./` | Directory containing `main.go` |
| `--output` | `./docs` | Swagger output directory |
| `--parseDependency` | `false` | Parse dependencies |
| `--parseInternal` | `false` | Parse internal packages |
| `--parseDepth` | `100` | Dependency parse depth |
| `--instanceName` | empty | Swagger instance name |

Examples:

```bash
nika swagger init --dir ./ --output ./docs --parseDependency --parseInternal
nika swagger init --parseDepth 200 --instanceName public
```

## Format annotations

```bash
nika swagger fmt
nika swagger fmt --dir ./src
```

The generator reads Swagger annotations such as `@Summary`, `@Param`, and
`@Router` from Go source files. AI-generated mock handlers include their own
Swagger annotations and should be followed by `nika swagger init`.
