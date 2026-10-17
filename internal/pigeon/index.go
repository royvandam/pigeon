package pigeon

import "net/http"

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

func (cfg config) renderIndex(w http.ResponseWriter, r *http.Request, a authentication, directory, message, fileError string, status int) {
	access, err := cfg.accessAccount(r.Context(), a.User)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	defer access.close()
	root, err := access.Files.open()
	if err != nil {
		writeStorageError(w, err)
		return
	}
	defer root.Close()
	explorer, err := readExplorer(root, directory, a.CSRFToken)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	used, err := access.Files.usage(r.Context())
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
		Username       string
		Admin          bool
		Message        string
		FileError      string
	}{
		explorerData:   explorer,
		QuotaHuman:     humanBytes(access.QuotaBytes),
		UsedHuman:      humanBytes(used),
		RemainingHuman: humanBytes(remainingQuota(access.QuotaBytes, used)),
		RemainingBytes: remainingQuota(access.QuotaBytes, used),
		Username:       a.User.Username,
		Admin:          a.User.Admin,
		Message:        message,
		FileError:      fileError,
	}
	renderTemplate(w, indexTmpl, data, status)
}
