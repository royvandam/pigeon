package server

import (
	"net/http"

	"github.com/royvandam/pigeon/internal/size"
	"github.com/royvandam/pigeon/pkg/auth"
)

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if !allowMethods(w, r, http.MethodGet) {
		return
	}
	a, ok := s.requireAuth(w, r, authOptions{Browser: true})
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	messages := map[string]string{"mkdir": "Folder created.", "rename": "Item renamed.", "move": "Item moved.", "delete": "Item deleted."}
	s.renderIndex(w, r, a, r.URL.Query().Get("dir"), messages[r.URL.Query().Get("result")], "", http.StatusOK)
}

func (s *Server) renderIndex(w http.ResponseWriter, r *http.Request, a auth.Authentication, directory, message, fileError string, status int) {
	access, err := s.files.Open(r.Context(), a.User)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	defer access.Close()
	explorer, err := readExplorer(r.Context(), access, directory, a.CSRFToken)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	usage, err := access.Usage(r.Context())
	if err != nil {
		http.Error(w, "failed to check storage usage", http.StatusInternalServerError)
		return
	}
	data := struct {
		explorerData
		QuotaHuman     string
		UsedHuman      string
		RemainingHuman string
		RemainingBytes int64
		AllocationUnit int64
		Username       string
		Admin          bool
		Message        string
		FileError      string
	}{
		explorerData:   explorer,
		QuotaHuman:     size.Format(usage.Quota),
		UsedHuman:      size.Format(usage.Used),
		RemainingHuman: size.Format(usage.Remaining()),
		RemainingBytes: usage.Remaining(),
		AllocationUnit: s.files.AllocationUnit(),
		Username:       a.User.Username,
		Admin:          a.User.Admin,
		Message:        message,
		FileError:      fileError,
	}
	renderTemplate(w, indexTmpl, data, status)
}
