package pigeon

import (
	"crypto/subtle"
	"errors"
	"log"
	"net/http"
	"net/url"
	"time"
)

const (
	sessionCookieName = "pigeon_session"
	loginCookieName   = "pigeon_login_csrf"
	maxFormBytes      = int64(64 << 10)
)

func sameOrigin(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host == r.Host && u.User == nil
	}
	return true
}

func validCSRF(expected, provided string) bool {
	return len(expected) == 43 && subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) == 1
}

func parseForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", uploadReadErrorStatus(err))
		return false
	}
	return true
}

func (cfg config) requestAuthentication(r *http.Request) (authentication, error) {
	if username, password, ok := r.BasicAuth(); ok {
		u, err := cfg.checkPassword(r, username, password)
		return authentication{User: u, Basic: true}, err
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return authentication{}, errInvalidCredentials
	}
	return cfg.store.session(r.Context(), cookie.Value)
}

type authOptions struct {
	Administrator bool
	Browser       bool
}

func (cfg config) requireAuth(w http.ResponseWriter, r *http.Request, options authOptions) (authentication, bool) {
	a, err := cfg.requestAuthentication(r)
	if err != nil {
		var limited *tooManyAttemptsError
		if errors.As(err, &limited) {
			setRetryAfter(w, limited)
			http.Error(w, limited.Error(), http.StatusTooManyRequests)
		} else if !errors.Is(err, errInvalidCredentials) {
			log.Printf("authenticate: %v", err)
			http.Error(w, "authentication unavailable", http.StatusInternalServerError)
		} else if options.Browser {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
		} else {
			w.Header().Set("WWW-Authenticate", `Basic realm="Pigeon", charset="UTF-8"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		}
		return authentication{}, false
	}
	if options.Administrator && !a.User.Admin {
		http.Error(w, errAdministratorRequired.Error(), http.StatusForbidden)
		return authentication{}, false
	}
	if r.Method == http.MethodPost {
		if !sameOrigin(r) {
			http.Error(w, "invalid request origin", http.StatusForbidden)
			return authentication{}, false
		}
		csrf := r.Header.Get("X-CSRF-Token")
		if r.URL.Path != "/upload" {
			if !parseForm(w, r) {
				return authentication{}, false
			}
			csrf = r.PostForm.Get("csrf_token")
		}
		if !a.Basic && !validCSRF(a.CSRFToken, csrf) {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return authentication{}, false
		}
	}
	return a, true
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: token, Path: "/", HttpOnly: true,
		Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode,
		MaxAge: int(sessionLifetime.Seconds()), Expires: time.Now().Add(sessionLifetime),
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Path: "/", HttpOnly: true,
		Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

func (cfg config) handleLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !allowMethods(w, r, http.MethodGet, http.MethodPost) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		if _, err := cfg.requestAuthentication(r); err == nil {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		token, err := randomToken()
		if err != nil {
			http.Error(w, "login unavailable", http.StatusInternalServerError)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: loginCookieName, Value: token, Path: "/login", HttpOnly: true,
			Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: 600})
		renderTemplate(w, loginTmpl, loginPage{CSRFToken: token}, http.StatusOK)
	case http.MethodPost:
		if !sameOrigin(r) || !parseForm(w, r) {
			if !sameOrigin(r) {
				http.Error(w, "invalid request origin", http.StatusForbidden)
			}
			return
		}
		cookie, err := r.Cookie(loginCookieName)
		if err != nil || !validCSRF(cookie.Value, r.PostForm.Get("csrf_token")) {
			http.Error(w, "invalid CSRF token; reload the login page", http.StatusForbidden)
			return
		}
		u, err := cfg.checkPassword(r, r.PostForm.Get("username"), r.PostForm.Get("password"))
		var limited *tooManyAttemptsError
		if errors.As(err, &limited) {
			setRetryAfter(w, limited)
			renderTemplate(w, loginTmpl, loginPage{CSRFToken: cookie.Value, Error: "Too many sign-in attempts. Try again in " + limited.wait() + "."}, http.StatusTooManyRequests)
			return
		}
		if errors.Is(err, errInvalidCredentials) {
			renderTemplate(w, loginTmpl, loginPage{CSRFToken: cookie.Value, Error: err.Error()}, http.StatusUnauthorized)
			return
		}
		if err != nil {
			log.Printf("login: %v", err)
			http.Error(w, "login unavailable", http.StatusInternalServerError)
			return
		}
		token, err := cfg.store.createSession(r.Context(), u)
		if err != nil {
			http.Error(w, "login unavailable; try again", http.StatusInternalServerError)
			return
		}
		setSessionCookie(w, r, token)
		http.SetCookie(w, &http.Cookie{Name: loginCookieName, Path: "/login", MaxAge: -1,
			HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

func (cfg config) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodPost) {
		return
	}
	a, ok := cfg.requireAuth(w, r, authOptions{Browser: true})
	if !ok {
		return
	}
	if err := cfg.store.deleteSession(r.Context(), a.TokenHash); err != nil {
		http.Error(w, "logout failed", http.StatusInternalServerError)
		return
	}
	clearSessionCookie(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
