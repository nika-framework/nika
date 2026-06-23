# Hot Reload

> **Coming Soon** — Built-in hot reload is not yet implemented.

Hot reload automatically restarts your application when source code changes.

## Current Alternative

Use [air](https://github.com/cosmtrek/air) for live reloading:

```bash
# Install air
go install github.com/cosmtrek/air@latest

# Create .air.toml configuration
cat > .air.toml << 'EOF'
[build]
  cmd = "go build -o ./tmp/main ."
  bin = "./tmp/main"
  watch_dir = "."
  watch_ext = ["go"]
  delay = 1000
EOF

# Run with hot reload
air
```

## Status

| Feature | Status |
|---------|--------|
| File watching | ⏳ Planned |
| Auto-restart | ⏳ Planned |
| Selective rebuild | ⏳ Planned |

!!! info "Tip"
    Use [air](https://github.com/cosmtrek/air) or [fresh](https://github.com/gravityblast/fresh) as a development tool until built-in hot reload is implemented.
