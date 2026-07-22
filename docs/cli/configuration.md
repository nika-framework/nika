# Project Configuration

Nika uses `.nika.toml` for application watch settings and AI provider settings.
The file is created automatically by `nika start --watch` when it does not
exist, or by `nika agent init <provider>` when configuring an AI provider.

## Complete example

```toml
#:schema https://json.schemastore.org/any.json

root = "."
testdata_dir = "testdata"
tmp_dir = "tmp"

[agent]
provider = "ollama"
model = "gemma3:4b"
base_url = "http://localhost:11434"
api_key_env = ""

[build]
cmd = "go run ."
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
```

## Agent settings

| Key | Description |
|-----|-------------|
| `provider` | `ollama`, `9router`, or `chatgpt` |
| `model` | Model identifier sent to the provider |
| `base_url` | Ollama or OpenAI-compatible API base URL |
| `api_key_env` | Environment variable containing the API key |

Use `nika agent init <provider>` to write safe defaults. Do not put an API key
literal in this file.

## Build and watch settings

`nika start --watch` uses these settings:

- `root`: directory watched and used as the process working directory.
- `build.cmd`: executable command prefix, such as `go run .`.
- `build.args`: additional arguments appended to the command.
- `build.bin`: reserved binary setting in the configuration format.
- `build.delay`: restart debounce delay in milliseconds.
- `build.exclude_dir`: directory names ignored by the watcher.
- `build.exclude_file`: exact file names ignored by the watcher.
- `build.exclude_regex`: regular expressions matched against file names.
- `build.include_ext`: file extensions that trigger a restart.
- `build.pre_cmd`: commands run before application start.
- `build.post_cmd`: commands run after the process exits.
- `build.env`: environment variables added to the application process.
- `build.env_files`: reserved environment-file setting in the configuration format.

The current process environment is preserved and merged with `build.env`.
