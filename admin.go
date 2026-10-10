package main

import (
	"net/http"
	"strconv"
)

type adminPage struct {
	Username  string
	CurrentID int64
	CSRFToken string
	Users     []user
	Error     string
	Message   string
}

func (cfg config) renderAdmin(w http.ResponseWriter, r *http.Request, a authentication, message, problem string, status int) {
	users, err := cfg.store.listUsers(r.Context())
	if err != nil {
		http.Error(w, "failed to load users", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	renderTemplate(w, adminTmpl, adminPage{
		Username: a.User.Username, CurrentID: a.User.ID, CSRFToken: a.CSRFToken,
		Users: users, Message: message, Error: problem,
	}, status)
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
		messages := map[string]string{"created": "User created.", "deleted": "User deleted. Their uploads have been preserved.", "reset": "Password reset. The user must sign in again."}
		cfg.renderAdmin(w, r, a, messages[r.URL.Query().Get("result")], "", http.StatusOK)
		return
	}

	var err error
	result := ""
	switch r.PostForm.Get("action") {
	case "create":
		_, err = cfg.store.createUser(r.Context(), r.PostForm.Get("username"), r.PostForm.Get("password"), r.PostForm.Get("admin") == "on")
		result = "created"
	case "delete", "reset":
		id, parseErr := strconv.ParseInt(r.PostForm.Get("user_id"), 10, 64)
		if parseErr != nil || id <= 0 {
			http.Error(w, "invalid user ID", http.StatusBadRequest)
			return
		}
		if r.PostForm.Get("action") == "delete" {
			err = cfg.store.deleteUser(r.Context(), id, a.User.ID)
			result = "deleted"
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
		cfg.renderAdmin(w, r, a, "", err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/admin?result="+result, http.StatusSeeOther)
}
