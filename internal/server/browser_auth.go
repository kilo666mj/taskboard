package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kilo666mj/oidcrp"
	"github.com/kilo666mj/taskboard/internal/config"
	"github.com/kilo666mj/taskboard/internal/store"
)

const (
	browserSessionCookie = "taskboard_session"
	browserSessionLife   = 30 * 24 * time.Hour
	desktopHandoffLife   = 2 * time.Minute
)

type browserSessions struct {
	store  *store.Store
	secure bool
	logger *slog.Logger
}

func newBrowserSessions(database *store.Store, secure bool, logger *slog.Logger) *browserSessions {
	return &browserSessions{store: database, secure: secure, logger: logger}
}

func (s *browserSessions) Valid(r *http.Request) bool {
	_, valid := s.identity(r.Context(), r)
	return valid
}

func (s *browserSessions) Issue(w http.ResponseWriter, r *http.Request, identity oidcrp.Identity) error {
	token, expires, err := s.store.CreateBrowserSession(r.Context(), storeIdentity(identity), browserSessionLife)
	if err != nil {
		return err
	}
	s.setCookie(w, token, expires)
	return nil
}

func (s *browserSessions) IssueDesktop(_ http.ResponseWriter, r *http.Request, identity oidcrp.Identity, handoff string) error {
	return s.store.CreateDesktopHandoff(r.Context(), handoff, storeIdentity(identity), desktopHandoffLife)
}

func (s *browserSessions) Clear(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(browserSessionCookie); err == nil {
		if err := s.store.DeleteBrowserSession(r.Context(), cookie.Value); err != nil {
			s.logger.Warn("delete browser session", "error", err)
		}
	}
	http.SetCookie(w, &http.Cookie{Name: browserSessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteStrictMode, MaxAge: -1})
}

func (s *browserSessions) identity(ctx context.Context, r *http.Request) (store.BrowserIdentity, bool) {
	cookie, err := r.Cookie(browserSessionCookie)
	if err != nil {
		return store.BrowserIdentity{}, false
	}
	identity, valid, err := s.store.BrowserSession(ctx, cookie.Value)
	if err != nil {
		s.logger.Warn("validate browser session", "error", err)
		return store.BrowserIdentity{}, false
	}
	return identity, valid
}

func (s *browserSessions) exchangeDesktop(w http.ResponseWriter, r *http.Request, code string) error {
	token, expires, _, err := s.store.ExchangeDesktopHandoff(r.Context(), code, browserSessionLife)
	if err != nil {
		return err
	}
	s.setCookie(w, token, expires)
	return nil
}

func (s *browserSessions) setCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{Name: browserSessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: int(browserSessionLife.Seconds())})
}

func storeIdentity(identity oidcrp.Identity) store.BrowserIdentity {
	return store.BrowserIdentity{Subject: identity.Subject, Email: identity.Email, Groups: identity.Groups}
}

func identityActor(identity store.BrowserIdentity) string {
	if email := strings.TrimSpace(identity.Email); email != "" {
		return email
	}
	return strings.TrimSpace(identity.Subject)
}

func validDesktopHandoff(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return false
		}
	}
	return true
}

func desktopSessionExchange(sessions *browserSessions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r.Header.Get("Origin"), r.Host) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request rejected"})
			return
		}
		var input struct {
			Code string `json:"code"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		if !validDesktopHandoff(input.Code) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid desktop sign-in handoff"})
			return
		}
		if err := sessions.exchangeDesktop(w, r, input.Code); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "desktop sign-in is pending or expired"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create desktop session"})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func desktopLoginComplete(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Taskboard sign-in complete</title></head><body><main><h1>Sign-in complete</h1><p>You can close this window and return to Taskboard.</p></main></body></html>`))
}

func logout(cfg config.Config, sessions *browserSessions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(origin, r.Host) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request rejected"})
			return
		}
		if cfg.CloudflareAccessEnabled() {
			writeJSON(w, http.StatusOK, map[string]string{"logout_url": "/cdn-cgi/access/logout"})
			return
		}
		sessions.Clear(w, r)
		w.WriteHeader(http.StatusNoContent)
	}
}
