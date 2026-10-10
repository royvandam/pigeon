package main

import (
	"bytes"
	_ "embed"
	"html/template"
	"net/http"
)

//go:embed templates/index.html
var indexHTML string

var indexTmpl = template.Must(template.New("index").Parse(indexHTML))

//go:embed templates/login.html
var loginHTML string

//go:embed templates/admin.html
var adminHTML string

var (
	loginTmpl = template.Must(template.New("login").Parse(loginHTML))
	adminTmpl = template.Must(template.New("admin").Parse(adminHTML))
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

func (cfg config) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a, ok := cfg.requireAuth(w, r, false, true)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	data := struct {
		MaxHuman  string
		MaxBytes  int64
		Username  string
		Admin     bool
		CSRFToken string
	}{
		MaxHuman:  humanBytes(cfg.maxBytes),
		MaxBytes:  cfg.maxBytes,
		Username:  a.User.Username,
		Admin:     a.User.Admin,
		CSRFToken: a.CSRFToken,
	}
	renderTemplate(w, indexTmpl, data, http.StatusOK)
}
