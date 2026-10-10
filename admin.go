package main

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type adminPage struct {
	Username           string
	CurrentID          int64
	CSRFToken          string
	Users              []adminUser
	DeletedUsers       []adminUser
	Error              string
	Message            string
	CreatedUser        *createdUserCredentials
	RecycleCandidateID int64
	RecycleCandidate   *adminUser
}

type createdUserCredentials struct {
	Username   string
	Passphrase string
	Recycled   bool
}

type adminUser struct {
	user
	UsedHuman  string
	QuotaHuman string
	QuotaInput string
}

func (cfg config) renderAdmin(w http.ResponseWriter, r *http.Request, a authentication, data adminPage, status int) {
	users, err := cfg.store.listAdminUsers(r.Context())
	if err != nil {
		http.Error(w, "failed to load users", http.StatusInternalServerError)
		return
	}
	var rows, deletedRows []adminUser
	for _, u := range users {
		used, err := cfg.filesFor(u).usage(r.Context())
		if err != nil {
			http.Error(w, "failed to check storage usage", http.StatusInternalServerError)
			return
		}
		row := adminUser{user: u, UsedHuman: humanBytes(used), QuotaHuman: humanBytes(u.QuotaBytes), QuotaInput: quotaInput(u.QuotaBytes)}
		if u.Deleted {
			deletedRows = append(deletedRows, row)
			if u.ID == data.RecycleCandidateID {
				data.RecycleCandidate = &row
			}
		} else {
			rows = append(rows, row)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	data.Username, data.CurrentID, data.CSRFToken = a.User.Username, a.User.ID, a.CSRFToken
	data.Users = rows
	data.DeletedUsers = deletedRows
	if data.RecycleCandidateID != 0 && data.RecycleCandidate == nil {
		data.Error = "That account has already been recycled or purged. Refresh the page and try again."
		status = http.StatusConflict
	}
	renderTemplate(w, adminTmpl, data, status)
}

func (cfg config) handleAdmin(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodGet, http.MethodPost) {
		return
	}
	a, ok := cfg.requireAuth(w, r, authOptions{Administrator: true, Browser: true})
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		messages := map[string]string{"created": "User created.", "deleted": "User deleted. Their uploads have been preserved.", "purged": "Account and uploads permanently deleted. The username and folder are available for reuse.", "reset": "Password reset. The user must sign in again.", "quota": "Disk quota updated."}
		cfg.renderAdmin(w, r, a, adminPage{Message: messages[r.URL.Query().Get("result")]}, http.StatusOK)
		return
	}

	command, err := parseAccountCommand(r.PostForm)
	if err != nil {
		cfg.renderAdmin(w, r, a, adminPage{Error: err.Error()}, http.StatusBadRequest)
		return
	}
	change, err := cfg.accounts().apply(r.Context(), a.User, command)
	if errors.Is(err, errPassphraseGeneration) {
		http.Error(w, errPassphraseGeneration.Error(), http.StatusInternalServerError)
		return
	}
	if command.action == "purge" && err != nil && !errors.Is(err, errDeletedUserNotFound) {
		log.Printf("purge account %d: %v", command.id, err)
		cfg.renderAdmin(w, r, a, adminPage{Error: "Unable to finish purging this account. Its username remains reserved. Some uploads may have been removed; resolve the storage problem and try again."}, http.StatusInternalServerError)
		return
	}
	if err != nil {
		if errors.Is(err, errUserConflict) {
			id, lookupErr := cfg.store.deletedUserIDForUsername(r.Context(), r.PostForm.Get("username"))
			if lookupErr == nil {
				cfg.renderAdmin(w, r, a, adminPage{RecycleCandidateID: id}, http.StatusOK)
				return
			}
			if !errors.Is(lookupErr, sql.ErrNoRows) {
				http.Error(w, "failed to check the reserved account", http.StatusInternalServerError)
				return
			}
		}
		cfg.renderAdmin(w, r, a, adminPage{Error: err.Error()}, http.StatusBadRequest)
		return
	}
	if change.Credentials != nil {
		// Credentials appear only in this uncached response, never a redirect,
		// cookie, or persistent page state.
		cfg.renderAdmin(w, r, a, adminPage{CreatedUser: &createdUserCredentials{
			Username: change.Credentials.User.Username, Passphrase: change.Credentials.Passphrase,
			Recycled: command.action == "recycle",
		}}, http.StatusOK)
		return
	}
	if change.ActorPasswordChanged {
		clearSessionCookie(w, r)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	results := map[string]string{"delete": "deleted", "reset": "reset", "quota": "quota", "purge": "purged"}
	http.Redirect(w, r, "/admin?result="+results[command.action], http.StatusSeeOther)
}

func parseAccountCommand(form url.Values) (accountCommand, error) {
	command := accountCommand{action: form.Get("action")}
	switch command.action {
	case "create":
		command.username = form.Get("username")
		command.administrator = form.Get("admin") == "on"
		command.quota = defaultUserQuota
		if value := strings.TrimSpace(form.Get("quota")); value != "" {
			quota, err := parseQuota(value)
			if err != nil {
				return command, err
			}
			command.quota = quota
		}
	case "delete", "reset", "quota", "recycle", "purge":
		id, err := strconv.ParseInt(form.Get("user_id"), 10, 64)
		if err != nil || id <= 0 {
			return command, errors.New("invalid user ID")
		}
		command.id = id
		command.password = form.Get("password")
		if command.action == "quota" {
			quota, err := parseQuota(form.Get("quota"))
			if err != nil {
				return command, err
			}
			command.quota = quota
		}
	default:
		return command, errors.New("unknown action")
	}
	return command, nil
}
