# Nika CLI

Nika CLI is a Go command-line tool for scaffolding Nika applications, generating
resource code, running applications, managing Swagger documentation, and using
AI-assisted project changes.

## Installation

```bash
go install github.com/nika-framework/nika-cli@latest
```

Or build the binary from this repository:

```bash
go build -o nika
```

## Command reference

| Command | Purpose | Documentation |
|---------|---------|---------------|
| `nika new` | Create an application from the official template | [new](new.md) |
| `nika generate` / `nika g` | Generate resources and individual layers | [generate](generate.md) |
| `nika agent` | Configure and run an AI provider | [agent](agent.md) |
| `nika start` | Run an application, optionally with hot reload | [start](start.md) |
| `nika swagger` | Initialize or format Swagger documentation | [swagger](swagger.md) |
| `nika version` / `nika v` | Print version information | [version](version.md) |

The root command also exposes Cobra's `completion` command. Run
`nika completion --help` for shell-specific instructions.

## Typical workflow

```bash
nika new my-app
cd my-app
go mod tidy
nika agent init ollama
nika agent "Create a news module with title, text, image, and tags"
nika start --watch
```

The project must have Go installed. `nika new` uses the official Git template,
`generate` requires a project-root `go.mod`, and the `ollama` provider requires
a local Ollama server.

## Related documentation

- [Configuration](configuration.md) - `.nika.toml`, build/watch settings, and AI providers.
- [Libraries](libraries.md) - current library support status.
- [Monorepo](monorepo.md) - current workspace support status.
