package server

import (
	"bytes"
	"html/template"
	"log/slog"
	"net/http"

	"github.com/royvandam/pigeon/web"
)

var applicationAssets = web.Assets

func pageTemplate(name string) *template.Template {
	return template.Must(template.New(name+".html").ParseFS(applicationAssets,
		"templates/"+name+".html", "templates/assets.html", "templates/controls.html",
		"templates/files.html", "templates/dialog.html", "templates/validation.html", "templates/shares.html",
		"templates/uploads.html"))
}

var (
	indexTmpl = pageTemplate("index")
	filesTmpl = indexTmpl.Lookup("fileList")
	loginTmpl = pageTemplate("login")
	adminTmpl = pageTemplate("admin")
	// uploadLinkTmpl is the page behind an upload link.
	uploadLinkTmpl = pageTemplate("upload_link")
)

type loginPage struct {
	CSRFToken string
	Error     string
}

func renderTemplate(w http.ResponseWriter, tmpl *template.Template, data any, status int) {
	var body bytes.Buffer
	if err := tmpl.Execute(&body, data); err != nil {
		slog.Error("template error", "err", err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	w.Write(body.Bytes())
}
