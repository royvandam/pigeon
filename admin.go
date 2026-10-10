package main

import (
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
)

type adminPage struct {
	Username    string
	CurrentID   int64
	CSRFToken   string
	Users       []adminUser
	Error       string
	Message     string
	CreatedUser *createdUserCredentials
}

type createdUserCredentials struct {
	Username   string
	Passphrase string
}

type adminUser struct {
	user
	UsedHuman  string
	QuotaHuman string
	QuotaInput string
}

func (cfg config) renderAdmin(w http.ResponseWriter, r *http.Request, a authentication, data adminPage, status int) {
	users, err := cfg.store.listUsers(r.Context())
	if err != nil {
		http.Error(w, "failed to load users", http.StatusInternalServerError)
		return
	}
	var rows []adminUser
	for _, u := range users {
		used, err := diskUsage(r.Context(), filepath.Join(cfg.dir, u.Slug))
		if err != nil {
			http.Error(w, "failed to check storage usage", http.StatusInternalServerError)
			return
		}
		rows = append(rows, adminUser{user: u, UsedHuman: humanBytes(used), QuotaHuman: humanBytes(u.QuotaBytes), QuotaInput: quotaInput(u.QuotaBytes)})
	}
	w.Header().Set("Cache-Control", "no-store")
	data.Username, data.CurrentID, data.CSRFToken = a.User.Username, a.User.ID, a.CSRFToken
	data.Users = rows
	renderTemplate(w, adminTmpl, data, status)
}

func (cfg config) handleAdmin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a, ok := cfg.requireAuth(w, r, true, true)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		messages := map[string]string{"created": "User created.", "deleted": "User deleted. Their uploads have been preserved.", "reset": "Password reset. The user must sign in again.", "quota": "Disk quota updated."}
		cfg.renderAdmin(w, r, a, adminPage{Message: messages[r.URL.Query().Get("result")]}, http.StatusOK)
		return
	}

	var err error
	result := ""
	switch r.PostForm.Get("action") {
	case "create":
		quota := defaultUserQuota
		if value := strings.TrimSpace(r.PostForm.Get("quota")); value != "" {
			quota, err = parseQuota(value)
		}
		if err == nil {
			passphrase, generationErr := generatePassphrase()
			if generationErr != nil {
				http.Error(w, "failed to generate passphrase", http.StatusInternalServerError)
				return
			}
			var created user
			created, err = cfg.store.createUserWithQuota(r.Context(), r.PostForm.Get("username"), passphrase, r.PostForm.Get("admin") == "on", quota)
			if err == nil {
				// Return the credential in the creation response only, without
				// putting it in redirect URLs, cookies, or persistent state.
				cfg.renderAdmin(w, r, a, adminPage{CreatedUser: &createdUserCredentials{
					Username: created.Username, Passphrase: passphrase,
				}}, http.StatusOK)
				return
			}
		}
	case "delete", "reset", "quota":
		id, parseErr := strconv.ParseInt(r.PostForm.Get("user_id"), 10, 64)
		if parseErr != nil || id <= 0 {
			http.Error(w, "invalid user ID", http.StatusBadRequest)
			return
		}
		if r.PostForm.Get("action") == "delete" {
			err = cfg.store.deleteUser(r.Context(), id, a.User.ID)
			result = "deleted"
		} else if r.PostForm.Get("action") == "quota" {
			quota, parseErr := parseQuota(r.PostForm.Get("quota"))
			err = parseErr
			if err == nil {
				err = cfg.store.setQuota(r.Context(), id, quota)
			}
			result = "quota"
		} else {
			err = cfg.store.resetPassword(r.Context(), id, r.PostForm.Get("password"))
			result = "reset"
			if err == nil && id == a.User.ID {
				clearSessionCookie(w, r)
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
		}
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	if err != nil {
		cfg.renderAdmin(w, r, a, adminPage{Error: err.Error()}, http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/admin?result="+result, http.StatusSeeOther)
}
