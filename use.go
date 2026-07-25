package nika

import "github.com/gin-gonic/gin"

// Use appends global middleware to the engine.
func (a *App) Use(middleware ...gin.HandlerFunc) gin.IRouter {
	a.engine.Use(middleware...)
	return a.engine
}
