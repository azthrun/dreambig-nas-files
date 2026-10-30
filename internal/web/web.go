// Package web holds the embedded HTML templates and static assets (ADR-0003). Because
// they live under internal/, the Dockerfile's existing COPY of internal/ ships them; no
// other directory needs adding to the Dockerfile or .dockerignore.
//
// Templates must contain no literal UI text: use {{t .Lang "area.key"}} and add the
// string to internal/i18n.
package web

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"

	"github.com/azthrun/dreambig-nas-files/internal/i18n"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Templates parses every page template. Each page is a named template such as
// "login.html"; shared pieces are "header" and "footer".
func Templates() (*template.Template, error) {
	t, err := template.New("").Funcs(template.FuncMap{"t": i18n.T}).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	return t, nil
}

// Static returns the static assets, rooted so "app.css" is at the top.
func Static() fs.FS {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err) // embedded directory always exists
	}
	return sub
}
