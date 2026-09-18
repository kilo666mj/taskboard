package server

import (
	"context"
	"errors"
	"html/template"
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
	desktopConfirmCookie = "taskboard_desktop_confirm"
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

func (s *browserSessions) IssueDesktop(w http.ResponseWriter, r *http.Request, identity oidcrp.Identity, handoff string) error {
	confirmation, err := oidcrp.NewDesktopConfirmation(handoff)
	if err != nil {
		return err
	}
	if err := s.store.CreateDesktopHandoff(r.Context(), handoff, confirmation.BrowserSecret, confirmation.VerificationCode, storeIdentity(identity), desktopHandoffLife); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: desktopConfirmCookie, Value: confirmation.BrowserSecret, Path: "/api/v1/auth/desktop", HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteStrictMode, MaxAge: int(desktopHandoffLife.Seconds())})
	return nil
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

func (s *browserSessions) pendingDesktop(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(desktopConfirmCookie)
	if err != nil || len(cookie.Value) < 32 || len(cookie.Value) > 128 {
		return "", false
	}
	return cookie.Value, true
}

func (s *browserSessions) clearDesktopConfirmation(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: desktopConfirmCookie, Value: "", Path: "/api/v1/auth/desktop", HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteStrictMode, MaxAge: -1})
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

func desktopVerificationCode(handoff string) string {
	return strings.ToUpper(handoff[:4] + "-" + handoff[4:8])
}

func desktopLoginComplete(sessions *browserSessions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		confirmation, ok := sessions.pendingDesktop(r)
		if !ok {
			desktopConfirmationPage(w, http.StatusGone, "Desktop sign-in expired", "Return to the Taskboard desktop app and start sign-in again.", "")
			return
		}
		verificationCode, err := sessions.store.PendingDesktopHandoff(r.Context(), confirmation)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				sessions.logger.Error("load desktop confirmation", "error", err)
			}
			sessions.clearDesktopConfirmation(w)
			desktopConfirmationPage(w, http.StatusGone, "Desktop sign-in expired", "Return to the Taskboard desktop app and start sign-in again.", "")
			return
		}
		desktopConfirmationPage(w, http.StatusOK, "Confirm desktop sign-in", "Approve only if the Taskboard desktop app you opened shows this exact code. If you did not start sign-in, cancel.", verificationCode)
	}
}

func desktopConfirm(sessions *browserSessions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r.Header.Get("Origin"), r.Host) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request rejected"})
			return
		}
		confirmation, ok := sessions.pendingDesktop(r)
		if !ok {
			desktopConfirmationPage(w, http.StatusGone, "Desktop sign-in expired", "Return to the Taskboard desktop app and start sign-in again.", "")
			return
		}
		if err := sessions.store.ConfirmDesktopHandoff(r.Context(), confirmation); err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				sessions.logger.Error("confirm desktop handoff", "error", err)
			}
			sessions.clearDesktopConfirmation(w)
			desktopConfirmationPage(w, http.StatusGone, "Desktop sign-in expired", "Return to the Taskboard desktop app and start sign-in again.", "")
			return
		}
		sessions.clearDesktopConfirmation(w)
		desktopConfirmationPage(w, http.StatusOK, "Desktop sign-in confirmed", "You can close this window and return to Taskboard.", "")
	}
}

func desktopCancel(sessions *browserSessions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r.Header.Get("Origin"), r.Host) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request rejected"})
			return
		}
		if confirmation, ok := sessions.pendingDesktop(r); ok {
			if err := sessions.store.CancelDesktopHandoff(r.Context(), confirmation); err != nil {
				sessions.logger.Error("cancel desktop handoff", "error", err)
			}
		}
		sessions.clearDesktopConfirmation(w)
		desktopConfirmationPage(w, http.StatusOK, "Desktop sign-in cancelled", "No desktop session was created. You can close this window.", "")
	}
}

func desktopConfirmationPage(w http.ResponseWriter, status int, title, message, verificationCode string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = desktopConfirmationTemplate.Execute(w, struct {
		Title            string
		Message          string
		VerificationCode string
	}{title, message, verificationCode})
}

var desktopConfirmationTemplate = template.Must(template.New("desktop-confirmation").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>{{.Title}}</title></head><body><main><h1>{{.Title}}</h1><p>{{.Message}}</p>{{if .VerificationCode}}<p><strong><code>{{.VerificationCode}}</code></strong></p><form method="post" action="/api/v1/auth/desktop/confirm"><button type="submit">Confirm sign-in</button></form><form method="post" action="/api/v1/auth/desktop/cancel"><button type="submit">Cancel</button></form>{{end}}</main></body></html>`))

func logout(cfg config.Config, sessions *browserSessions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !safeBrowserMutation(r) {
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
