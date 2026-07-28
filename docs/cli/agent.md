# `nika agent`

The `agent` command is the AI interface for Nika CLI. It reads the provider and
model from `.nika.toml` and runs a **tool-calling loop** against your project:
the model inspects files, edits them, scaffolds modules with Nika's own
templates, and runs builds to check its work, repeating until the task is done.

Any instruction works — not only module generation.

```bash
nika agent init <provider>     # configure once per project
nika agent models              # what Ollama models you have, and which can call tools
nika agent "<instruction>"     # run one instruction
nika agent                     # interactive session
nika agent start               # chat page in the browser
nika agent files               # install editor AI files (.github)
```

## Configure a provider

```bash
nika agent init ollama
nika agent init chatgpt
nika agent init 9router
nika agent init claude
```

The command creates or updates the `[agent]` section in `.nika.toml` while
preserving every other section.

For Ollama it inspects the models you actually have installed and selects one,
rather than writing a fixed model name that may not exist on your machine:

```text
Installed Ollama models:
  deepseek-r1:7b              4.4 GB  native tools
  gemma3:4b                   3.1 GB  emulated tools
  llama3:8b                   4.3 GB  emulated tools

✔ Selected deepseek-r1:7b — it supports native tool calling.
```

Tool-capable models win; among those, a known coding or function-calling family
wins, and within a family the larger variant. Override by editing `model` in
`.nika.toml`.

### Providers

| Provider | Default model | Default endpoint | API key |
|----------|---------------|------------------|---------|
| `ollama` | best installed model | `http://localhost:11434` | Not required |
| `chatgpt` | `gpt-4o-mini` | OpenAI API | `OPENAI_API_KEY` |
| `9router` | `openai/gpt-4o-mini` | OpenRouter OpenAI-compatible API | `OPENROUTER_API_KEY` |
| `claude` | `claude-sonnet-4-5` | Anthropic API | `ANTHROPIC_API_KEY` |

Aliases are accepted: `openai` → `chatgpt`, `openrouter` → `9router`,
`anthropic` → `claude`, `ollema` → `ollama`.

API keys are never written to `.nika.toml`; only the environment variable name
is stored. Set the variable before running the command for cloud providers.

## Models without tool calling

Many local models — `gemma3`, `llama3`, most older builds — expose no
function-calling API at all. They still work: the agent describes the tools in
the prompt and asks for a single JSON object per reply, then parses that back
into the same tool calls the native path produces. Everything above the
provider is identical, and the header shows `(emulated tools)`.

Which mode is used is decided automatically:

1. Ollama is asked what the model supports (`/api/show`).
2. A model without `tools` goes straight to the JSON protocol.
3. A model that claims support but rejects a tools request is switched over and
   retried, so the failure never reaches you.
4. A model that claims support and then *narrates* the edit instead of calling
   a tool is nudged once and moved to the JSON protocol, which states the
   contract far more bluntly.

See which of your models are which:

```bash
nika agent models
```

```text
  deepseek-r1:7b              4.4 GB  native tools
  gemma3:4b                   3.1 GB  emulated tools

Best for the agent: deepseek-r1:7b
```

Emulation makes small models usable, not equal. A 4B model calls tools reliably
but struggles to reproduce an exact string for `edit_file`; a 7B reasoning model
may still describe work rather than do it. For real editing work prefer
`qwen2.5-coder:7b` or larger, or a cloud provider.

### Nothing is reported as done unless it happened

If a run finishes with a summary claiming files were added or updated, but no
file was actually written, the answer is contradicted rather than passed
through:

```text
Added a Phone field to the User model and verified the changes.

⚠ Nothing was actually changed — no file was written during this run.
```

This catches the most common weak-model failure, where the model narrates a
plausible edit it never performed.

## Run an instruction

```bash
nika agent "add a price field (float64, required) to the product model and update the DTOs, response and mapper"
nika agent "generate a category module with name and slug for sqlite"
nika agent "why does the user controller return 500 on update?"
nika agent "run go build ./... and fix what it reports"
```

Each step is printed as it happens:

```text
  · Thinking with ollama / qwen2.5-coder:7b
  ⚙ project_info
    ✔ module path: nikaapp …
  ⚙ search {"pattern": "type User struct", "path": "apps/micro-grpc/src", "ext": ".go"}
    ✔ apps/micro-grpc/src/user/schema/user.model.go:5: type User struct { …
  ⚙ read_file {"path": "apps/micro-grpc/src/user/schema/user.model.go"}
  ⚙ edit_file {"path": "apps/micro-grpc/src/user/schema/user.model.go", …}
    ✔ edited apps/micro-grpc/src/user/schema/user.model.go (1 replacement(s))
  ⚙ run_command {"command": "go build ./apps/micro-grpc/..."}
    ✔ command succeeded with no output

Added a Phone field to the micro-grpc User model and confirmed the service still builds.

  📝 changed: apps/micro-grpc/src/user/schema/user.model.go
```

Use `-q` / `--quiet` to print only the final answer.

## Interactive session

Running the command with no prompt starts a REPL that keeps the conversation,
so follow-ups build on what came before:

```bash
nika agent
```

```text
  🤖 Nika agent — ollama / qwen2.5-coder:7b
     Project: /Users/me/my-app
     Type your instruction. /reset clears the conversation, /exit quits.

› add a price field to the product model
› now add it to the create DTO too
```

| Input | Effect |
|-------|--------|
| `/reset` | Clear the conversation; the project is untouched |
| `/changed` | List the files changed this session |
| `/exit`, `/quit` | Leave |

## `nika agent start` — browser chat

```bash
nika agent start
```

Starts a local chat server and opens it. Everything typed in the page is
carried out **in the directory the command was started from**, and each tool
call is streamed back to the page as it runs.

```text
  🤖 Nika agent chat is running
     Project : /Users/me/my-app
     Model   : ollama / qwen2.5-coder:7b
     Open    : http://127.0.0.1:7777/?token=e955a61eeaa57fbd…
```

The page has a sidebar and two tabs.

**Sidebar** — every chat in the session, newest first, titled from its first
message. `＋ New chat` starts an independent conversation; `×` deletes one.
Each chat keeps its own history, so a long refactor and a quick question do not
contaminate each other. They all act on the same project directory. The footer
shows the project path, the model, and the detected microservices.

**Chat tab** — the conversation. Every tool call is an expandable entry with
its arguments and result, and each turn ends with the list of files that
changed. Above the input is a single-line strip of suggestions that scrolls
sideways.

**Commands tab** — the CLI's own generators as forms, run directly without the
model:

| Group | Commands |
|-------|----------|
| Generate | Resource (full module), Controller, Service, DTOs, Response + mapper |
| Database | Migration, Seed, Run migrations, Roll back, Migration status, Run seeds, Seed status |
| Workspace | List apps, Set default app, Sync workspace |
| Tools | Generate Swagger docs, Build, Test |

The Resource form has the same repeating field editor the interactive CLI
prompts for — name, Go type, required — with the type list following the
database you pick. In a workspace, an **App** dropdown lists the real services.
Output appears in the dialog. These are the same code paths `nika g` uses, so
the result is identical: framework-correct code, registered in the right
`app.module.go`.

Use the Commands tab when you know exactly what you want, and the Chat tab when
you don't — a form cannot express "add a price field everywhere it belongs".

| Flag | Default | Purpose |
|------|---------|---------|
| `-p`, `--port` | `7777` | Port to listen on; `0` picks a free one |
| `--host` | `127.0.0.1` | Address to bind |
| `--open` | `true` | Open the default browser |

The server binds to loopback and requires the token embedded in the URL it
prints, because it holds write access to your source tree. One run at a time
across the whole server — including the Commands tab — since two things editing
the same tree would interleave writes.

## Tools

| Tool | Changes files | Purpose |
|------|---------------|---------|
| `project_info` | no | Module path, layout, apps, and the modules in each |
| `list_dir` | no | List a directory, optionally recursively |
| `read_file` | no | Read a file with line numbers; supports `offset`/`limit` |
| `search` | no | Regex search across the project |
| `write_file` | yes | Create a file or replace it entirely |
| `edit_file` | yes | Replace an exact string; must match uniquely |
| `nika_generate` | yes | Scaffold a full resource with Nika's templates |
| `run_command` | yes | Run a build or inspection command |

`edit_file` requires `old_string` to appear verbatim. When it does not, the
match is retried with whitespace collapsed — a model reproducing a line from
memory usually gets the words right and the spacing wrong — and the replacement
is re-indented to the file's own indentation. An ambiguous fuzzy match is
refused rather than guessed, and a failed one lists the file's nearest lines so
the next attempt can copy real text. Omitting `new_string` is rejected: a
half-formed call must not become a silent deletion.

`nika_generate` takes the same inputs as `nika g res` — module, database,
fields, and in a workspace the target app — so AI-generated modules are
identical to hand-generated ones and are registered in the right
`app.module.go`.

The system prompt carries a live snapshot of the project: module path, layout,
every app with its source folder and import prefix, and the modules each
already contains. The model knows about `apps/api` from the first turn instead
of spending steps discovering it.

## Safety

The agent is confined to the project directory. Paths that escape it — `../`,
or absolute paths outside the root — are refused.

`run_command` accepts an allowlist only: `go`, `gofmt`, `goimports`, `swag`,
`nika`, `ls`, `cat`, `head`, `tail`, `wc`, `grep`, `rg`, `find`, `tree`, and the
read-only git subcommands (`status`, `diff`, `log`, `show`, `ls-files`,
`branch`). Shell operators (`;` `&` `|` `>` `<` `` ` `` `$`) are rejected, since
commands run without a shell. Extend the list per project:

```toml
[agent]
  allow_commands = ["make", "docker compose ps"]
```

| Flag | Purpose |
|------|---------|
| `--read-only` | Inspect the project but refuse every mutating tool |
| `--allow-any-command` | Remove the `run_command` allowlist |
| `-C`, `--dir` | Work in another directory instead of the current one |

A failing tool call is handed back to the model as content rather than ending
the run, so a wrong path or a stale `old_string` is something it can correct on
the next step. The loop stops after `agent.max_steps` rounds (default 25).

## Install project AI files

```bash
nika agent files
```

Installs Nika-specific instructions, prompts, and skills under `.github` for
editor agents such as Copilot and Cursor. This is unrelated to `nika agent
init`, which configures the CLI's own provider.

> In earlier versions this was `nika agent` with no arguments. That now starts
> an interactive session; use `nika agent files` for the `.github` files.

## Route generation

`nika ollema` still carries the older mock-route workflow, which plans a route
from a model definition and registers it on a controller. See
[ollema](../commands/ollema.md). For new work prefer `nika agent`, which can
add routes as ordinary edits and is not limited to MongoDB models.

## See also

- [Configuration](configuration.md) - the `[agent]` section.
- [Workspaces](monorepo.md) - how the agent picks a microservice.
- [`nika generate`](generate.md) - the templates `nika_generate` uses.
