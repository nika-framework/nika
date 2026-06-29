package nika

import (
	"reflect"

	"github.com/gin-gonic/gin"
)

type App struct {
	engine    *gin.Engine
	container map[reflect.Type]interface{}
}

func NewApp() *App {
	return &App{
		engine:    gin.Default(),
		container: make(map[reflect.Type]interface{}),
	}
}
