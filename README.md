# Nika
Nika is a modern backend framework for Go, designed for scalability, clean architecture, and developer productivity.
## Documents

[Click me](https://nika-framework.github.io/nika/)

## example
```go
package main

import (
	"fmt" 
	"github.com/nika-framework/nika" 
)

func main() { 
	app := nika.NewApp()

	rootModule := src.NewAppModule()
	app.LoadModule(rootModule)

	port := "3001"
	fmt.Printf("🚀 ٔNika is running on http://localhost:%s\n", port)
	app.Listen(":" + port)
}
```
## run docs
```ssh
mkdocs serve
```