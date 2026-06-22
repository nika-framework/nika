package nika

import "fmt"

func (a *App) Listen(addr string) error {
    fmt.Printf("\n***🚀Nika is running on http://localhost%s *****\n", addr)
    return a.engine.Run(addr)
}