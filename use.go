package nika

import "github.com/gin-gonic/gin"

func (a *App) Use(middleware ...gin.HandlerFunc) gin.IRouter {
	a.engine.Use(middleware...)
	return a.engine
}
