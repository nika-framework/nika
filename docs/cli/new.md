# `nika new`

Create a new Nika application from the official Git template.

```bash
nika new <app-name>
```

Example:

```bash
nika new my-app
cd my-app
go mod download
go mod tidy
```

The command:

1. Validates the application name.
2. Clones `https://github.com/nika-framework/nika-app.git` into the target directory.
3. Removes the template's `.git` directory.
4. Initializes a fresh Git repository when Git is available.
5. Replaces the template module name with the new application name.
6. Updates imports in `go.mod`, `main.go`, and `src/app.controller.go`.

After creation, configure an agent with `nika agent init ollama` or another
provider.
