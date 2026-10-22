package server

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/royvandam/pigeon/internal/size"
	"github.com/royvandam/pigeon/pkg/account"
	"github.com/royvandam/pigeon/pkg/auth"
	"github.com/royvandam/pigeon/pkg/quota"
)

type adminPage struct {
	Username           string
	CurrentID          int64
	CSRFToken          string
	Users              []adminUser
	DeletedUsers       []adminUser
	Error              string
	Message            string
	Credentials        *credentialNotice
	SignedOut          bool
	RecycleCandidateID int64
	RecycleCandidate   *adminUser
}

type credentialNotice struct {
	Username   string
	Passphrase string
	Heading    string
}

type adminUser struct {
	account.User
	UsedHuman  string
	QuotaHuman string
	QuotaInput string
}

func (s *Server) renderAdmin(w http.ResponseWriter, r *http.Request, a auth.Authentication, data adminPage, status int) {
	users, err := s.accounts.List(r.Context())
	if err != nil {
		http.Error(w, "failed to load users", http.StatusInternalServerError)
		return
	}
	var rows, deletedRows []adminUser
	for _, u := range users {
		used, err := s.files.Usage(r.Context(), u)
		if err != nil {
			http.Error(w, "failed to check storage usage", http.StatusInternalServerError)
			return
		}
		row := adminUser{User: u, UsedHuman: size.Format(used), QuotaHuman: size.Format(u.QuotaBytes), QuotaInput: quotaInput(u.QuotaBytes)}
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

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodGet, http.MethodPost) {
		return
	}
	a, ok := s.requireAuth(w, r, authOptions{Administrator: true, Browser: true})
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		messages := map[string]string{"deleted": "User deleted. Their uploads have been preserved.", "purged": "Account and uploads permanently deleted. The username and folder are available for reuse.", "quota": "Disk quota updated."}
		s.renderAdmin(w, r, a, adminPage{Message: messages[r.URL.Query().Get("result")]}, http.StatusOK)
		return
	}

	command, err := parseAccountCommand(r.PostForm)
	if err != nil {
		s.renderAdmin(w, r, a, adminPage{Error: err.Error()}, http.StatusBadRequest)
		return
	}
	change, err := s.applyAccountCommand(r.Context(), a.User, command)
	if errors.Is(err, account.ErrPassphraseGeneration) {
		http.Error(w, account.ErrPassphraseGeneration.Error(), http.StatusInternalServerError)
		return
	}
	if command.action == "purge" && err != nil && !errors.Is(err, account.ErrDeletedUserNotFound) {
		log.Printf("purge account %d: %v", command.id, err)
		s.renderAdmin(w, r, a, adminPage{Error: "Unable to finish purging this account. Its username remains reserved. Some uploads may have been removed; resolve the storage problem and try again."}, http.StatusInternalServerError)
		return
	}
	if err != nil {
		if errors.Is(err, account.ErrUserConflict) {
			id, lookupErr := s.accounts.DeletedIDForUsername(r.Context(), r.PostForm.Get("username"))
			if lookupErr == nil {
				s.renderAdmin(w, r, a, adminPage{RecycleCandidateID: id}, http.StatusOK)
				return
			}
			if !errors.Is(lookupErr, sql.ErrNoRows) {
				http.Error(w, "failed to check the reserved account", http.StatusInternalServerError)
				return
			}
		}
		s.renderAdmin(w, r, a, adminPage{Error: err.Error()}, http.StatusBadRequest)
		return
	}
	if change.Credentials != nil {
		// Credentials appear only in this uncached response, never a redirect,
		// cookie, or persistent page state.
		data := adminPage{Credentials: &credentialNotice{
			Username: change.Credentials.User.Username, Passphrase: change.Credentials.Passphrase,
			Heading: map[string]string{"create": "User created", "recycle": "User recycled", "reset": "Password reset"}[command.action],
		}, SignedOut: change.ActorPasswordChanged}
		if data.SignedOut {
			s.clearSessionCookie(w, r)
			w.Header().Set("Cache-Control", "no-store")
			renderTemplate(w, adminTmpl, data, http.StatusOK)
		} else {
			s.renderAdmin(w, r, a, data, http.StatusOK)
		}
		return
	}
	results := map[string]string{"delete": "deleted", "quota": "quota", "purge": "purged"}
	http.Redirect(w, r, "/admin?result="+results[command.action], http.StatusSeeOther)
}

func parseAccountCommand(form url.Values) (accountCommand, error) {
	command := accountCommand{action: form.Get("action")}
	switch command.action {
	case "create":
		command.username = form.Get("username")
		command.administrator = form.Get("admin") == "on"
		command.quota = quota.DefaultQuota
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
