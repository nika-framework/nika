# `nika agent`

The `agent` command is the unified AI interface for Nika CLI. It reads the
provider and model from `.nika.toml`, sends normal prompts to that provider,
and detects prompts that request module or route changes.

## Configure a provider

```bash
nika agent init ollama
nika agent init 9router
nika agent init chatgpt
```

The command creates or updates the `[agent]` section in `.nika.toml` while
preserving the build configuration.

### Providers

| Provider | Default model | Default endpoint | API key |
|----------|---------------|------------------|---------|
| `ollama` | `gemma3:4b` | `http://localhost:11434` | Not required |
| `9router` | `openai/gpt-4o-mini` | OpenRouter OpenAI-compatible API | `OPENROUTER_API_KEY` |
| `chatgpt` | `gpt-4o-mini` | OpenAI API | `OPENAI_API_KEY` |

API keys are never written to `.nika.toml`; only the environment variable name
is stored. Set the variable before running the command for cloud providers.

## Run a prompt

```bash
nika agent "Explain dependency injection in Go"
```

The command requires an initialized `.nika.toml`. A normal prompt is sent to
the configured provider and the response is printed.

## Generate a module

Prompts containing module creation intent are converted into a structured
resource definition and passed to the same generator used by `nika generate res`:

```bash
nika agent "Create a news module with title, text, image, and tags"
```

The generated module includes schema, DTO, service, controller, response, and
module files. The model name and field types are validated before files are
written.

## Add a route

Prompts that mention a route, endpoint, mock, or mock data use the route workflow:

```bash
nika agent "Add a mock data creation route to the news module"
```

The workflow:

1. Identifies the requested module and verifies `src/<module>` exists.
2. Checks the module model and currently supports MongoDB models.
3. Finds controller structs under `src/<module>/controllers`.
4. Asks which controller to use when more than one controller exists.
5. Uses the model definition and AI plan to create `controllers/mock.go`.
6. Registers the route on the selected controller.

The generated handler uses the controller field and handler method separately:

```go
CreateMock func(*gin.Context) `route:"POST:/newss/mock"`

// in the constructor
c.CreateMock = c.CreateMockHandler
```

The generated `CreateMockHandler` includes Swagger annotations and calls the
module's existing `Create` service method.

## Install project AI files

Running the command without a prompt installs Nika-specific agent files under
`.github`:

```bash
nika agent
```

This is separate from provider initialization. Use `nika agent init <provider>`
to configure `.nika.toml`, and `nika agent` without arguments to install project
instructions, prompts, and skills.
