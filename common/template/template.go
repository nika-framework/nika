// Package template wires an html/template set into the Gin engine.
//
// html/template is used deliberately and must not be swapped for text/template.
// html/template escapes interpolated values according to where they appear —
// element text, attribute value, URL, JavaScript literal, CSS — which is what
// stops user-supplied data from becoming markup. text/template has the same API
// and would compile without a single change, while silently turning every page
// that renders user input into a stored-XSS vector.
package template

import (
	"fmt"
	"html/template"

	"github.com/nika-framework/nika"
)

type Template struct {
	app *nika.App
}

// Setup parses pattern and installs the resulting template set, panicking on
// failure.
//
// Kept panicking for convenience: templates are a build-time asset, and in the
// common case a bad glob means the binary was deployed without its views, which
// should stop the process rather than serve blank pages. Use SetupE where a
// failure should be handled — an optional plugin, a hot-reload path, or a test.
func Setup(app *nika.App, pattern string) *Template {
	t, err := SetupE(app, pattern)
	if err != nil {
		panic(err.Error())
	}
	return t
}

// SetupE parses pattern and installs the resulting template set, returning any
// error.
func SetupE(app *nika.App, pattern string) (*Template, error) {
	if app == nil {
		return nil, fmt.Errorf("template: app must not be nil")
	}

	tmpl, err := template.ParseGlob(pattern)
	if err != nil {
		// ParseGlob returns an error both for a malformed template and for a glob
		// that matched nothing; say which pattern was used, since a typo'd path is
		// by far the most common cause.
		return nil, fmt.Errorf("template: parse glob %q: %w", pattern, err)
	}

	app.GetGin().SetHTMLTemplate(tmpl)

	cfg := &Template{
		app: app,
	}

	app.RegisterSingleton(cfg)

	return cfg, nil
}

// Load replaces the installed template set, panicking on failure. It mirrors
// Setup; see LoadE to handle the error.
func (t *Template) Load(pattern string) {
	if err := t.LoadE(pattern); err != nil {
		panic(err.Error())
	}
}

// LoadE replaces the installed template set, returning any error.
func (t *Template) LoadE(pattern string) error {
	tmpl, err := template.ParseGlob(pattern)
	if err != nil {
		return fmt.Errorf("template: parse glob %q: %w", pattern, err)
	}
	t.app.GetGin().SetHTMLTemplate(tmpl)
	return nil
}
