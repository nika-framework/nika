# `nika start`

Run a Nika application in normal or watch mode.

```bash
nika start [file-or-dir]
nika start --watch [file-or-dir]
```

With no target, the default target is `./main.go`.

## Normal mode

Normal mode runs `go run <target>` once and returns when the process exits:

```bash
nika start
nika start ./main.go
```

## Watch mode

Watch mode reads `.nika.toml`, runs the configured build command, and restarts
it when an included file changes:

```bash
nika start --watch
```

The default watch settings are:

- Root: `.`
- Command: `go run .`
- Delay: `1000` milliseconds
- Included extension: `.go`
- Excluded directories: `docs`, `tmp`, `vendor`, `testdata`, `.git`, `cache`
- Excluded file pattern: `^\\.`

Watch mode supports `pre_cmd`, `post_cmd`, environment variables, included
extensions, excluded files, excluded directories, and exclude regular
expressions. See [Configuration](configuration.md).
