package swagger

import (
	"github.com/sajadweb/nika"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
)

type Config struct {
	Path string
}

func Setup(app *nika.App, cfg *Config) {
	var path = cfg.Path
	if cfg.Path == "" {
		path = "/swagger/*any"
	}
	app.GET(path, ginSwagger.WrapHandler(
		swaggerFiles.Handler,
	),
	)
}
