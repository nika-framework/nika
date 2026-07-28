# Project Configuration

Nika uses `.nika.toml` for the project layout, application watch settings, and
AI provider settings. It is created automatically by `nika start`,
`nika agent init <provider>`, or `nika app sync` when it does not exist.

All four sections are read and written by one decoder, so a command that
touches one section preserves the others.

## Complete example

```toml
#:schema https://json.schemastore.org/any.json

root = "."
testdata_dir = "testdata"
tmp_dir = "tmp"

[workspace]
  mode = "microservice"
  apps_dir = "apps"
  apps = ["api", "micro-grpc", "micro-redis", "micro-tcp"]
  default_app = "api"
  src_dir = "src"

[agent]
  provider = "ollama"
  model = "qwen2.5-coder:7b"
  base_url = "http://localhost:11434"
  api_key_env = ""
  max_steps = 25
  allow_commands = []

[build]
  cmd = "go run ./apps/api/main.go"
  args = []
  bin = ""
  delay = 1000
  exclude_dir = ["docs", "tmp", "vendor", "testdata", ".git", "cache"]
  exclude_file = []
  exclude_regex = ["^\\."]
  include_ext = [".go"]
  pre_cmd = []
  post_cmd = []
  env_files = []
  [build.env]

[apps]
  [apps.api]
    cmd = "go run ./apps/api/main.go"
  [apps.micro-grpc]
    cmd = "go run ./apps/micro-grpc/main.go"
  [apps.micro-redis]
    cmd = "go run ./apps/micro-redis/main.go"
  [apps.micro-tcp]
    cmd = "go run ./apps/micro-tcp/main.go"
```

A single-application project has no `[apps]` table and a much shorter
`[workspace]`:

```toml
[workspace]
  mode = "single"
  apps_dir = "apps"
  src_dir = "src"

[build]
  cmd = "go run ."
```

## Workspace settings

| Key | Default | Description |
|-----|---------|-------------|
| `mode` | detected | `single`, `microservice`, or empty to detect from disk |
| `apps_dir` | `apps` | Directory holding the per-service folders |
| `apps` | detected | Service names; refreshed from disk on every run |
| `default_app` | first gateway found | App that `nika start` runs without `-a` |
| `src_dir` | `src` | Name of the module folder inside an app |

Detection always wins on existence: a service deleted from disk is dropped from
`apps`, and one added by hand is picked up without editing this file. An
explicit `mode = "single"` pins the classic layout even when `apps/` exists.

> **`src_dir` is a folder name, not a path.** It is the module folder *inside*
> each app, so the only sensible value is `src`. Writing `src_dir = "apps/api/"`
> — a natural guess, since that is where the source lives — would produce paths
> like `apps/micro-grpc/apps/api/product`. Both `src_dir` and `apps_dir` are
> now validated: a value containing a slash is rejected, the default is used
> instead, and the CLI says so:
>
> ```text
> ⚠ workspace.src_dir in .nika.toml must be a single folder name, not a path
>   — got "apps/api/". Using "src" instead.
> ```
>
> Run `nika app sync` to write the corrected value back.

Write these with [`nika app`](app.md) rather than by hand:

```bash
nika app sync          # match what is on disk
nika app use api       # set default_app and build.cmd
```

See [Workspaces](monorepo.md).

## Agent settings

| Key | Default | Description |
|-----|---------|-------------|
| `provider` | — | `ollama`, `chatgpt`, `9router`, or `claude` |
| `model` | per provider | Model identifier sent to the provider |
| `base_url` | per provider | Ollama, OpenAI-compatible, or Anthropic base URL |
| `api_key_env` | per provider | Environment variable holding the API key |
| `max_steps` | `25` | Maximum tool-call rounds in one agent run |
| `allow_commands` | `[]` | Extra prefixes accepted by the `run_command` tool |

Use `nika agent init <provider>` to write safe defaults. Do not put an API key
literal in this file — only the variable name is stored, and the value is read
from the environment at run time.

`allow_commands` extends the built-in allowlist rather than replacing it:

```toml
[agent]
  allow_commands = ["make", "docker compose ps"]
```

See [`nika agent`](agent.md).

## Per-app build settings

`[apps.<name>]` overrides the root `[build]` section for one service. Only the
keys you set are overridden; everything else falls back to `[build]`.

| Key | Description |
|-----|-------------|
| `cmd` | Run command for this service |
| `args` | Extra arguments appended to the command |
| `bin` | Binary setting for this service |
| `pre_cmd` | Commands run before this service starts |
| `post_cmd` | Commands run after this service exits |
| `env` | Environment variables merged over `build.env` |

```toml
[apps.micro-grpc]
  cmd = "go run ./apps/micro-grpc/main.go"
  [apps.micro-grpc.env]
    GRPC_ADDR = "127.0.0.1:50052"
```

`nika app sync` fills in a `cmd` for every service it finds and leaves any you
have customised alone.

## Build and watch settings

`nika start` uses these settings in both normal and watch mode:

- `root`: directory watched and used as the process working directory.
- `build.cmd`: the command to run, such as `go run .`.
- `build.args`: additional arguments appended to the command.
- `build.bin`: reserved binary setting in the configuration format.
- `build.delay`: restart debounce delay in milliseconds (watch mode).
- `build.exclude_dir`: directory names ignored by the watcher.
- `build.exclude_file`: exact file names ignored by the watcher.
- `build.exclude_regex`: regular expressions matched against file names.
- `build.include_ext`: file extensions that trigger a restart.
- `build.pre_cmd`: commands run before application start.
- `build.post_cmd`: commands run after the process exits.
- `build.env`: environment variables added to the application process.
- `build.env_files`: reserved environment-file setting in the configuration format.

The current process environment is preserved and merged with `build.env`, then
with the selected app's `[apps.<name>.env]`.

In a workspace, `build.cmd` should point at the default app's `main.go`.
`nika app sync` and `nika app use` keep it correct; leaving it at `go run .`
means `nika start` tries to run a project root that has no main package.
