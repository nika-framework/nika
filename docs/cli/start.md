# `nika start`

Run a Nika application in normal or watch mode.

```bash
nika start [file-or-dir]
nika start -a <app>
nika start --watch
```

## Choosing what runs

With no arguments the command runs the `[build] cmd` from `.nika.toml`. In a
microservice workspace that is the default app's `main.go`, so you never have
to remember each service's path.

Precedence, highest first:

1. an explicit path argument
2. the `-a` / `--app` flag
3. `workspace.default_app` in `.nika.toml`
4. an interactive prompt, when the workspace has several apps and no default

```bash
nika start                        # the default app
nika start -a micro-grpc          # one specific service
nika start ./apps/api/main.go     # an explicit path
```

```text
▶️  Starting micro-grpc: go run ./apps/micro-grpc/main.go
✅ SQL Database connected (sqlite3)
```

Partial app names are accepted when unambiguous, so `-a grpc` resolves to
`micro-grpc`. Set the default once with:

```bash
nika app use api
```

See [Workspaces](monorepo.md) and [`nika app`](app.md).

## Normal mode

Normal mode runs the resolved command once in `root` and returns when the
process exits:

```bash
nika start
nika start -a api
```

## Watch mode

Watch mode runs the same command and restarts it when an included file
changes:

```bash
nika start --watch
nika start -a micro-grpc --watch
```

The default watch settings are:

- Root: `.`
- Command: `go run .` in a single-app project, the app's `main.go` in a workspace
- Delay: `1000` milliseconds
- Included extension: `.go`
- Excluded directories: `docs`, `tmp`, `vendor`, `testdata`, `.git`, `cache`
- Excluded file pattern: `^\\.`

Watch mode supports `pre_cmd`, `post_cmd`, environment variables, included
extensions, excluded files, excluded directories, and exclude regular
expressions — taken from the root `[build]` section merged with the selected
app's `[apps.<name>]` override. See [Configuration](configuration.md).

Press Ctrl+C to stop; the running process is terminated before the CLI exits.
