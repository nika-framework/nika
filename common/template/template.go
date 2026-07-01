package template 

import (
	"html/template"
 
	"github.com/sajadweb/nika"
)

type Template struct {
	app *nika.App
}

func Setup(app *nika.App, pattern string) *Template {
	t := template.Must(template.ParseGlob(pattern))

	app.GetGin().SetHTMLTemplate(t)

	cfg := &Template{
		app: app,
	}

	app.RegisterSingleton(cfg)

	return cfg
}

func (t *Template) Load(pattern string) {
	tmpl := template.Must(template.ParseGlob(pattern))
	t.app.GetGin().SetHTMLTemplate(tmpl)
}