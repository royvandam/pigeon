package server

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"time"

	"github.com/royvandam/pigeon/internal/token"
	"github.com/royvandam/pigeon/pkg/account"
	"github.com/royvandam/pigeon/pkg/auth"
)

const (
	sessionCookieName = "pigeon_session"
	loginCookieName   = "pigeon_login_csrf"
	maxFormBytes      = int64(64 << 10)
)

func validCSRF(expected, provided string) bool {
	return len(expected) == token.Length && subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) == 1
}

func (s *Server) parseForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	clearDeadline := limitBodyRead(w, s.formTimeout)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", uploadReadErrorStatus(err))
		return false
	}
	clearDeadline()
	return true
}

func (s *Server) requestAuthentication(r *http.Request) (auth.Authentication, error) {
	if username, password, ok := r.BasicAuth(); ok {
		u, err := s.checkPassword(r, username, password)
		return auth.Authentication{User: u, Basic: true}, err
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return auth.Authentication{}, account.ErrInvalidCredentials
	}
	return s.sessions.Lookup(r.Context(), cookie.Value)
}

// navigation reports whether r loads a page in a browser tab. Browsers send
// Fetch Metadata only to HTTPS and loopback origins, but send
// Upgrade-Insecure-Requests with every navigation.
func navigation(r *http.Request) bool {
	return r.Header.Get("Sec-Fetch-Mode") == "navigate" || r.Header.Get("Upgrade-Insecure-Requests") == "1"
}

// browserRequest reports whether r comes from a browser rather than an API
// client. Scripts on plain HTTP origins send no Fetch Metadata, so the
// explorer's scripts mark their requests with X-Requested-With.
func browserRequest(r *http.Request) bool {
	return navigation(r) || r.Header.Get("Sec-Fetch-Mode") != "" || r.Header.Get("X-Requested-With") == "XMLHttpRequest"
}

type authOptions struct {
	Administrator bool
	Browser       bool
}

func (s *Server) requireAuth(w http.ResponseWriter, r *http.Request, options authOptions) (auth.Authentication, bool) {
	a, err := s.requestAuthentication(r)
	if err != nil {
		var limited *auth.TooManyAttemptsError
		if errors.As(err, &limited) {
			setRetryAfter(w, limited)
			http.Error(w, limited.Error(), http.StatusTooManyRequests)
		} else if !errors.Is(err, account.ErrInvalidCredentials) {
			serverError(w, r, "authentication unavailable", err)
		} else if options.Browser || (r.Method == http.MethodGet && navigation(r)) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
		} else {
			// Browsers answer a Basic challenge with their own sign-in prompt and
			// then send the password with every request, which skips the CSRF
			// check and outlives Sign out. Only API clients are challenged.
			if !browserRequest(r) {
				w.Header().Set("WWW-Authenticate", `Basic realm="Pigeon", charset="UTF-8"`)
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		}
		return auth.Authentication{}, false
	}
	if options.Administrator && !a.User.Admin {
		http.Error(w, account.ErrAdministratorRequired.Error(), http.StatusForbidden)
		return auth.Authentication{}, false
	}
	if r.Method == http.MethodPost {
		if !s.sameOrigin(r) {
			http.Error(w, "invalid request origin", http.StatusForbidden)
			return auth.Authentication{}, false
		}
		csrf := r.Header.Get("X-CSRF-Token")
		if r.URL.Path != "/upload" {
			if !s.parseForm(w, r) {
				return auth.Authentication{}, false
			}
			csrf = r.PostForm.Get("csrf_token")
		}
		if !a.Basic && !validCSRF(a.CSRFToken, csrf) {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return auth.Authentication{}, false
		}
	}
	return a, true
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: token, Path: "/", HttpOnly: true,
		Secure: s.secureCookies(r), SameSite: http.SameSiteLaxMode,
		MaxAge: int(auth.SessionLifetime.Seconds()), Expires: time.Now().Add(auth.SessionLifetime),
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Path: "/", HttpOnly: true,
		Secure: s.secureCookies(r), SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !allowMethods(w, r, http.MethodGet, http.MethodPost) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		if _, err := s.requestAuthentication(r); err == nil {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		csrf, err := token.New()
		if err != nil {
			serverError(w, r, "login unavailable", err)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: loginCookieName, Value: csrf, Path: "/login", HttpOnly: true,
			Secure: s.secureCookies(r), SameSite: http.SameSiteStrictMode, MaxAge: 600})
		renderTemplate(w, loginTmpl, loginPage{CSRFToken: csrf}, http.StatusOK)
	case http.MethodPost:
		if !s.sameOrigin(r) || !s.parseForm(w, r) {
			if !s.sameOrigin(r) {
				http.Error(w, "invalid request origin", http.StatusForbidden)
			}
			return
		}
		cookie, err := r.Cookie(loginCookieName)
		if err != nil || !validCSRF(cookie.Value, r.PostForm.Get("csrf_token")) {
			http.Error(w, "invalid CSRF token; reload the login page", http.StatusForbidden)
			return
		}
		u, err := s.checkPassword(r, r.PostForm.Get("username"), r.PostForm.Get("password"))
		var limited *auth.TooManyAttemptsError
		if errors.As(err, &limited) {
			setRetryAfter(w, limited)
			renderTemplate(w, loginTmpl, loginPage{CSRFToken: cookie.Value, Error: "Too many sign-in attempts. Try again in " + limited.Wait() + "."}, http.StatusTooManyRequests)
			return
		}
		if errors.Is(err, account.ErrInvalidCredentials) {
			renderTemplate(w, loginTmpl, loginPage{CSRFToken: cookie.Value, Error: err.Error()}, http.StatusUnauthorized)
			return
		}
		if err != nil {
			serverError(w, r, "login unavailable", err)
			return
		}
		token, err := s.sessions.Create(r.Context(), u)
		if err != nil {
			serverError(w, r, "login unavailable; try again", err)
			return
		}
		s.setSessionCookie(w, r, token)
		http.SetCookie(w, &http.Cookie{Name: loginCookieName, Path: "/login", MaxAge: -1,
			HttpOnly: true, Secure: s.secureCookies(r), SameSite: http.SameSiteStrictMode})
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodPost) {
		return
	}
	a, ok := s.requireAuth(w, r, authOptions{Browser: true})
	if !ok {
		return
	}
	if err := s.sessions.Delete(r.Context(), a.TokenHash); err != nil {
		serverError(w, r, "logout failed", err)
		return
	}
	s.clearSessionCookie(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
