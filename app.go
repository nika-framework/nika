package nika

import (
	"os"

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
	// Run Gin in release mode in production for lower overhead and faster writes.
	if os.Getenv("GIN_MODE") == "" {
		gin.SetMode(gin.ReleaseMode)
	}
	return &App{
		engine:    gin.New(),
		container: make(map[reflect.Type]interface{}),
		guards:    make(map[string]GuardFunc),
	}
}
