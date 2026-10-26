// Package web — HTML-шаблоны веб-интерфейса, встроенные в бинарник.
package web

import (
	"embed"
	"html/template"
)

//go:embed templates/*.html
var templatesFS embed.FS

// Templates разбирает шаблоны upload.html и gallery.html.
func Templates() (*template.Template, error) {
	return template.ParseFS(templatesFS, "templates/*.html")
}
