package agentboard

import (
	"net/http"
	"time"
)

// sessionCookieName is the cookie that carries a session ID. See
// startSession for its attributes (HttpOnly, SameSite=Strict, optionally
// Secure).
const sessionCookieName = "agentboard_session"

type credentialsRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// handleRegister handles POST /api/auth/register. On success it also logs
// the new account in (starts a session and sets the cookie): registering
// and then requiring a second, separate login is friction the UI does not
// need to impose, and the request already proved the caller knows the
// password.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[credentialsRequest](w, r)
	if !ok {
		return
	}
	u, err := s.o.Board.Register(req.Email, req.Password)
	if err != nil {
		s.reply(w, http.StatusCreated, nil, err)
		return
	}
	if err := s.startSession(w, u.Email); err != nil {
		s.o.Logger.Error("agentboard: starting session after register failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "internal error"})
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

// handleLogin handles POST /api/auth/login. Failed attempts are throttled
// per (normalized) email by a simple in-memory limiter; see loginLimiter's
// doc comment for exactly what that does and does not defend against.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[credentialsRequest](w, r)
	if !ok {
		return
	}
	key := normalizeEmail(req.Email)
	if !s.logins.allowed(key) {
		writeJSON(w, http.StatusTooManyRequests, apiError{Error: "too many failed login attempts for this account; try again later"})
		return
	}
	u, err := s.o.Board.Authenticate(req.Email, req.Password)
	if err != nil {
		s.logins.recordFailure(key)
		s.reply(w, http.StatusOK, nil, err)
		return
	}
	s.logins.recordSuccess(key)
	if err := s.startSession(w, u.Email); err != nil {
		s.o.Logger.Error("agentboard: starting session failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, u)
}

// handleLogout handles POST /api/auth/logout: it invalidates the session
// server-side (so the same cookie cannot be replayed after logout, unlike a
// client that only forgets a stateless token) and clears the cookie.
// Logging out with no session, or an already-expired one, is not an error.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		s.sessions.invalidate(c.Value)
	}
	s.clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleMe handles GET /api/auth/me: "who is logged in on this browser", if
// anyone. The UI uses it to show the current user's email in the header and
// to decide whether to show the login form.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	email, ok := s.currentUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, apiError{Error: "not logged in"})
		return
	}
	u, ok := s.o.Board.User(email)
	if !ok {
		// The session outlived its account (no account-deletion API exists
		// yet, but be defensive rather than assume it never will).
		s.clearSessionCookie(w)
		writeJSON(w, http.StatusUnauthorized, apiError{Error: "not logged in"})
		return
	}
	writeJSON(w, http.StatusOK, u)
}

// currentUser reports the logged-in email for r's session cookie, if any.
func (s *Server) currentUser(r *http.Request) (string, bool) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return "", false
	}
	return s.sessions.validate(c.Value)
}

// startSession creates a session and sets its cookie: HttpOnly (JavaScript
// cannot read it, closing off a whole class of XSS session theft),
// SameSite=Strict (the browser never sends it on a cross-site request, so
// another site cannot ride a logged-in browser's session) and Secure when
// ServerOptions.CookieSecure is set (only sent over HTTPS - off by default
// for the plain-HTTP loopback deployment this project ships; see
// docs/PITFALLS.md).
func (s *Server) startSession(w http.ResponseWriter, email string) error {
	id, err := s.sessions.create(email)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: id, Path: "/",
		HttpOnly: true, Secure: s.o.CookieSecure, SameSite: http.SameSiteStrictMode,
		MaxAge: int(s.sessions.ttl / time.Second),
	})
	return nil
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.o.CookieSecure, SameSite: http.SameSiteStrictMode,
	})
}
