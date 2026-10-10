package main

import (
	"bytes"
	"embed"
	"html/template"
	"net/http"
)

// Templates and local assets are part of the binary; deployment needs no asset directory.
//
//go:embed templates/*.html static/*.js static/*.css
var applicationAssets embed.FS

func pageTemplate(name string) *template.Template {
	return template.Must(template.New(name+".html").ParseFS(applicationAssets,
		"templates/"+name+".html", "templates/assets.html", "templates/controls.html",
		"templates/files.html", "templates/dialog.html", "templates/validation.html", "templates/shares.html"))
}

var (
	indexTmpl = pageTemplate("index")
	filesTmpl = indexTmpl.Lookup("fileList")
	loginTmpl = pageTemplate("login")
	adminTmpl = pageTemplate("admin")
)

type loginPage struct {
	CSRFToken string
	Error     string
}

func renderTemplate(w http.ResponseWriter, tmpl *template.Template, data any, status int) {
	var body bytes.Buffer
	if err := tmpl.Execute(&body, data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	w.Write(body.Bytes())
}
