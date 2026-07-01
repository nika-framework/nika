# Nika
Nika is a modern backend framework for Go, designed for scalability, clean architecture, and developer productivity.
## example
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
	fmt.Printf("🚀 ٔNika is running on http://localhost:%s\n", port)
	app.Listen(":" + port)
}
```
## run docs
```ssh
mkdocs serve
```