package main

import (
	"bytes"
	_ "embed"
	"html/template"
	"net/http"
	"path/filepath"
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
	used, err := diskUsage(r.Context(), filepath.Join(cfg.dir, a.User.Slug))
	if err != nil {
		http.Error(w, "failed to check storage usage", http.StatusInternalServerError)
		return
	}
	data := struct {
		QuotaHuman     string
		UsedHuman      string
		RemainingHuman string
		RemainingBytes int64
		Username       string
		Admin          bool
		CSRFToken      string
	}{
		QuotaHuman:     humanBytes(a.User.QuotaBytes),
		UsedHuman:      humanBytes(used),
		RemainingHuman: humanBytes(remainingQuota(a.User.QuotaBytes, used)),
		RemainingBytes: remainingQuota(a.User.QuotaBytes, used),
		Username:       a.User.Username,
		Admin:          a.User.Admin,
		CSRFToken:      a.CSRFToken,
	}
	renderTemplate(w, indexTmpl, data, http.StatusOK)
}
