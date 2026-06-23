# 🚀 Nika Framework

<p align="center">
  <strong>Nika is a modern backend framework for Go, designed for scalability, clean architecture, and developer productivity.</strong>
</p>

<p align="center">
  <a href="https://github.com/sajadweb/nika/releases">
    <img src="https://img.shields.io/github/v/release/sajadweb/nika?style=flat-square" alt="Release" />
  </a>
  <a href="https://github.com/sajadweb/nika/blob/main/LICENSE">
    <img src="https://img.shields.io/github/license/sajadweb/nika?style=flat-square" alt="License" />
  </a>
  <a href="https://golang.org">
    <img src="https://img.shields.io/badge/Go-1.25+-00ADD8?style=flat-square&logo=go" alt="Go version" />
  </a>
  <a href="https://github.com/sajadweb/nika">
    <img src="https://img.shields.io/github/stars/sajadweb/nika?style=flat-square" alt="Stars" />
  </a>
</p>

---

## Overview

**Nika** is a progressive Go web framework that helps you build efficient, scalable, and maintainable server-side applications.

Built on top of **[Gin](https://github.com/gin-gonic/gin)**, Nika leverages Gin's high-performance HTTP routing while adding a layer of organization through Modules, Providers, Controllers, and a powerful DI container.

## Features

- 🏗️ **Modular Architecture** — Organize your application into self-contained modules
- 💉 **Dependency Injection** — Built-in DI container with automatic resolution
- 🎯 **Struct-tag Routing** — Define routes directly on controller fields
- 🔌 **Common Packages** — Ready-to-use integrations for MongoDB, Redis, Cache, Validation, and Config
- ⚡ **High Performance** — Powered by Gin's fast HTTP engine
- 📦 **Generic Repository** — Type-safe MongoDB repository with full CRUD operations
- ✅ **Validation** — Struct validation with custom rules (e.g., Iranian mobile, ObjectId)

## Quick Start

=== "Install"
    ```bash
    go get github.com/sajadweb/nika
    ```

=== "Create an app"
    ```go
    package main

    import (
        "fmt"
        "github.com/sajadweb/nika"
    )

    func main() {
        app := nika.NewApp()

        rootModule := src.NewAppModule()
        app.LoadModule(rootModule)

        port := "3001"
        fmt.Printf("🚀 Nika is running on http://localhost:%s\n", port)
        app.Listen(":" + port)
    }
    ```

## Architecture

Nika follows the **Module → Provider → Controller** pattern:

```
┌─────────────────────────────────────────────────┐
│                      App                        │
│                                                  │
│  ┌──────────────────────────────────────────┐   │
│  │              Root Module                  │   │
│  │                                          │   │
│  │  ┌─────────┐  ┌──────────────────────┐  │   │
│  │  │ Module  │  │      Module           │  │   │
│  │  │  Auth   │  │       Users          │  │   │
│  │  │         │  │                      │  │   │
│  │  │ Provs   │  │  Controllers         │  │   │
│  │  │Ctrls    │  │  Providers           │  │   │
│  │  └─────────┘  └──────────────────────┘  │   │
│  └──────────────────────────────────────────┘   │
│                                                  │
│  ┌──────────────────────────────────────────┐   │
│  │              DI Container                │   │
│  └──────────────────────────────────────────┘   │
│                                                  │
│  ┌──────────────────────────────────────────┐   │
│  │           Gin HTTP Engine                │   │
│  └──────────────────────────────────────────┘   │
└─────────────────────────────────────────────────┘
```

## Common Packages

| Package | Description |
|---------|-------------|
| `common/config` | Environment-based configuration with `.env` support |
| `common/mongodb` | MongoDB connection and generic repository pattern |
| `common/cache` | Cache abstraction with Redis and File drivers |
| `common/validator` | Struct validation with custom rules |
| `common/response` | Standardized JSON response helpers |

## Documentation

Browse the documentation using the sidebar navigation. Start with [First Steps](overview/first-steps.md) to create your first application.

## License

MIT © [Sajad Mohammadi Nejad](https://github.com/sajadweb)
