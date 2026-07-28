# Nika CLI

Nika CLI is a Go command-line tool for scaffolding Nika applications, generating
resource code, running applications, managing Swagger documentation, and using
AI-assisted project changes.

## Installation

```bash
go install github.com/nika-framework/nika-cli@latest
```

The CLI requires Go 1.23 or newer. The same installation command works in
PowerShell, Command Prompt, macOS, and Linux. If Windows reports errors from
`internal/start`, update to a release containing the Windows process-handling
fix and run the command again.

Or build the binary from this repository:

```bash
go build -o nika
```

## Command reference

| Command | Purpose | Documentation |
|---------|---------|---------------|
| `nika new` | Create an application from the official template | [new](new.md) |
| `nika generate` / `nika g` | Generate resources and individual layers | [generate](generate.md) |
| `nika agent` | Run the AI agent, or open the browser console | [agent](agent.md) |
| `nika app` | Inspect and configure the apps in a workspace | [app](app.md) |
| `nika start` | Run an application, optionally with hot reload | [start](start.md) |
| `nika migrate` | Apply, roll back, or inspect migrations | [generate](generate.md#migrations-and-seeds) |
| `nika seed` | Run database seeders | [generate](generate.md#migrations-and-seeds) |
| `nika ollema` | Legacy single-prompt Ollama command | [ollema](../commands/ollema.md) |
| `nika swagger` | Initialize or format Swagger documentation | [swagger](swagger.md) |
| `nika version` / `nika v` | Print version information | [version](version.md) |

The root command also exposes Cobra's `completion` command. Run
`nika completion --help` for shell-specific instructions.

## Project layouts

The CLI supports two layouts and detects which one a project uses from disk:

| Layout | Modules live in | Entry point |
|--------|-----------------|-------------|
| Single application | `src/<module>/` | `main.go` |
| Microservice workspace | `apps/<service>/src/<module>/` | `apps/<service>/main.go` |

In a workspace, `generate`, `agent`, and `start` all operate on one service at
a time. Pass `-a`/`--app` to say which, or answer the prompt. See
[Workspaces](monorepo.md).

## Typical workflow

Single application:

```bash
nika new my-app
cd my-app
go mod tidy
nika g res user -d sqlite
nika start --watch
```

Microservice workspace:

```bash
nika app list                    # see the services
nika g res user -a micro-grpc    # generate into one of them
nika app use api                 # pick the default for `nika start`
nika start --watch
```

With the AI agent:

```bash
nika agent init ollama
nika agent "Create a news module with title, text, image, and tags"
nika agent "add a published_at field to the news model and update the DTOs"
nika agent start                 # or drive it from a chat page
```

The project must have Go installed. `nika new` uses the official Git template,
`generate` requires a project-root `go.mod`, and the `ollama` provider requires
a local Ollama server running a tool-capable model.

## Related documentation

- [Configuration](configuration.md) - `.nika.toml`, workspace, build/watch, and AI settings.
- [Workspaces](monorepo.md) - the microservice layout and how it is detected.
- [Libraries](libraries.md) - current library support status.
