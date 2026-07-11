package nika

import (
	"reflect"

	"github.com/gin-gonic/gin"
)
type GuardFunc func(args []string) gin.HandlerFunc
type App struct {
	engine    *gin.Engine
	container map[reflect.Type]interface{}
	guards    map[string]GuardFunc 
}

func NewApp() *App {
	return &App{
		engine:    gin.Default(),
		container: make(map[reflect.Type]interface{}),
		guards:    make(map[string]GuardFunc),
	}
}
