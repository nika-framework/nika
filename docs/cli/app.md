# `nika app`

Inspect and configure the apps in a project. Aliases: `nika apps`,
`nika workspace`.

```bash
nika app list
nika app use <name>
nika app sync
```

The layout itself is detected from disk, so these commands are for looking at
what was found and choosing the default service — not for declaring the
structure. See [Workspaces](monorepo.md) for how detection works.

## `nika app list`

Print every app with its source folder, run command, and the modules it
already contains.

```bash
nika app list
```

```text
Microservice workspace — module nikaapp

 * api              src: apps/api/src             run: go run ./apps/api/main.go
                    modules: [user-grpc user-redis user-tcp]
   micro-grpc       src: apps/micro-grpc/src      run: go run ./apps/micro-grpc/main.go
                    modules: [user]

 * = default for `nika start` (change it with `nika app use <name>`)
```

In a single-application project the same command reports one app:

```text
Single application — module nikaapp

   app              src: src                      run: go run .
                    modules: [common user]
```

## `nika app use`

Set the app that `nika start` runs when no `-a` flag is given. This writes
`workspace.default_app` and points `build.cmd` at that service.

```bash
nika app use api
```

```text
Default app is now "api" (go run ./apps/api/main.go)
```

Partial names work when unambiguous: `nika app use grpc` resolves to
`micro-grpc`.

## `nika app sync`

Rewrite `.nika.toml` to match what is on disk. Run it after adding or removing
a service under `apps/`.

```bash
nika app sync
```

```text
Updated .nika.toml: 4 app(s) — [api micro-grpc micro-redis micro-tcp]
Default app: api → go run ./apps/api/main.go
```

Sync:

- records `workspace.mode`, `workspace.apps`, and `workspace.apps_dir`;
- gives every service an `[apps.<name>]` entry with a run command, leaving any
  command you customised alone;
- drops `[apps.*]` entries for services that no longer exist;
- picks a default app if none is set, preferring a gateway named `api`,
  `gateway`, `http`, `web`, or `app`, and otherwise the first alphabetically;
- points the root `[build] cmd` at the default app.

That last step is why sync matters: without it, `[build] cmd` in a workspace
usually still reads `go run .`, and the project root has no main package to
run.

`nika generate` runs sync automatically in a workspace, so in practice you only
need it after restructuring by hand.

## Choosing the target for one command

Precedence, highest first:

1. the `-a` / `--app` flag
2. the `NIKA_APP` environment variable
3. the only app, when there is one
4. `workspace.default_app` (for `nika start`)
5. an interactive prompt

```bash
nika g res user -a api
NIKA_APP=micro-grpc nika g res order
```

## See also

- [Workspaces](monorepo.md) - layout detection and the microservice model.
- [Configuration](configuration.md) - the `[workspace]` and `[apps.*]` sections.
- [`nika start`](start.md) - running one service.
- [`nika generate`](generate.md) - generating into one service.
