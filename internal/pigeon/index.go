package pigeon

import (
	"net/http"

	"github.com/royvandam/pigeon/pkg/auth"
	"github.com/royvandam/pigeon/pkg/quota"
)

func (cfg config) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if !allowMethods(w, r, http.MethodGet) {
		return
	}
	a, ok := cfg.requireAuth(w, r, authOptions{Browser: true})
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	messages := map[string]string{"mkdir": "Folder created.", "rename": "Item renamed.", "move": "Item moved.", "delete": "Item deleted."}
	cfg.renderIndex(w, r, a, r.URL.Query().Get("dir"), messages[r.URL.Query().Get("result")], "", http.StatusOK)
}

func (cfg config) renderIndex(w http.ResponseWriter, r *http.Request, a auth.Authentication, directory, message, fileError string, status int) {
	access, err := cfg.accessAccount(r.Context(), a.User)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	defer access.close()
	tree, err := access.Files.open(r.Context())
	if err != nil {
		writeStorageError(w, err)
		return
	}
	defer tree.Close()
	explorer, err := readExplorer(r.Context(), tree, directory, a.CSRFToken)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	used, err := access.used(r.Context())
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
		QuotaHuman:     humanBytes(access.QuotaBytes),
		UsedHuman:      humanBytes(used),
		RemainingHuman: humanBytes(quota.Remaining(access.QuotaBytes, used)),
		RemainingBytes: quota.Remaining(access.QuotaBytes, used),
		AllocationUnit: access.Files.unit,
		Username:       a.User.Username,
		Admin:          a.User.Admin,
		Message:        message,
		FileError:      fileError,
	}
	renderTemplate(w, indexTmpl, data, status)
}
