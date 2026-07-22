# `nika generate`

Generate Nika resource code from templates.

```bash
nika generate <type> <module>
nika g <type> <module>
```

The command must run from a project root containing `go.mod`. Module names are
lowercase and may contain letters, digits, and underscores; they must begin
with a lowercase letter.

## Generation types

| Type | Alias | Generated files |
|------|-------|-----------------|
| `res` | `r` | Full resource: schema, DTOs, services, controllers, responses, module |
| `controller` | `c` | Controller base and CRUD handlers |
| `service` | `s` | Service base and CRUD methods |
| `dto` | `d` | Create, update, find-one, and list DTOs |
| `response` | `rs` | Response models and mapper |

Examples:

```bash
nika generate res user
nika g r product
nika generate controller order
nika generate service order
nika generate dto order
nika generate response order
```

## Full resource generation

`res` currently selects MongoDB, then interactively asks for fields. For each
field, the generator asks for:

- Field name in snake case.
- Type: `string`, `int`, `int64`, `float64`, `bool`, `time.Time`,
  `primitive.ObjectID`, `[]string`, or `map[string]any`.
- Whether the field is required.

Finish field input by entering `done` or an empty field name. The generated
resource includes:

```text
src/<module>/
├── <module>.module.go
├── schema/
├── dto/
├── services/
├── controllers/
└── response/
```

The generator does not automatically register the new module in the main
application module; follow the generated reminder and add the module to
`src/app.module.go`.

## AI-assisted generation

After configuring an agent, a natural-language module request can replace the
interactive field collection:

```bash
nika agent init ollama
nika agent "Create a news module with title, text, image, and tags"
```

The AI response is constrained to the supported field types and validated before
the standard resource templates run.
