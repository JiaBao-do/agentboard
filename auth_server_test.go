package agentboard_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/JiaBao-do/agentboard"
)

func TestRegisterLoginLogoutMeThroughClient(t *testing.T) {
	ts, _ := newServer(t, agentboard.ServerOptions{})
	c := agentboard.NewClient(ts.URL, "")
	ctx := context.Background()

	if _, err := c.Me(ctx); apiStatus(err) != http.StatusUnauthorized {
		t.Fatalf("Me before login: %v", err)
	}

	u, err := c.Register(ctx, "alice@example.com", "correct horse battery")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if u.Email != "alice@example.com" {
		t.Fatalf("Register returned %q", u.Email)
	}

	// Register also logs in: no separate Login call needed.
	if me, err := c.Me(ctx); err != nil || me.Email != "alice@example.com" {
		t.Fatalf("Me after Register: %+v err=%v", me, err)
	}

	if err := c.Logout(ctx); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := c.Me(ctx); apiStatus(err) != http.StatusUnauthorized {
		t.Fatalf("Me after Logout: %v", err)
	}

	// A separate Client (its own cookie jar) must log in explicitly.
	c2 := agentboard.NewClient(ts.URL, "")
	if _, err := c2.Me(ctx); apiStatus(err) != http.StatusUnauthorized {
		t.Fatalf("Me on a fresh client: %v", err)
	}
	if _, err := c2.Login(ctx, "alice@example.com", "correct horse battery"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if me, err := c2.Me(ctx); err != nil || me.Email != "alice@example.com" {
		t.Fatalf("Me after Login: %+v err=%v", me, err)
	}

	if _, err := c2.Login(ctx, "alice@example.com", "wrong password"); apiStatus(err) != http.StatusUnauthorized {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err := c2.Login(ctx, "nobody@example.com", "whatever"); apiStatus(err) != http.StatusUnauthorized {
		t.Fatalf("unknown email: %v", err)
	}
}

func TestRegisterDuplicateThroughAPI(t *testing.T) {
	ts, _ := newServer(t, agentboard.ServerOptions{})
	c := agentboard.NewClient(ts.URL, "")
	ctx := context.Background()
	if _, err := c.Register(ctx, "dup@example.com", "a fine password"); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if _, err := c.Register(ctx, "dup@example.com", "a different password"); apiStatus(err) != http.StatusConflict {
		t.Fatalf("duplicate Register: %v", err)
	}
}

// TestSessionCookieAttributes checks the session cookie is HttpOnly and
// SameSite=Strict, so it is invisible to page JavaScript (closing off XSS
// session theft) and never sent on a cross-site request (closing off CSRF
// riding a logged-in session). Secure defaults to false, matching the
// plain-HTTP loopback deployment this project ships; see docs/PITFALLS.md.
func TestSessionCookieAttributes(t *testing.T) {
	ts, _ := newServer(t, agentboard.ServerOptions{})
	resp, body := do(t, http.MethodPost, ts.URL+"/api/auth/register",
		`{"email":"cookie@example.com","password":"a fine password"}`, jsonContentType)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register status %d: %s", resp.StatusCode, body)
	}
	var found *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == "agentboard_session" {
			found = ck
		}
	}
	if found == nil {
		t.Fatal("no agentboard_session cookie set")
	}
	if !found.HttpOnly {
		t.Error("session cookie is not HttpOnly")
	}
	if found.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite = %v, want Strict", found.SameSite)
	}
	if found.Secure {
		t.Error("session cookie is Secure by default; want false unless CookieSecure is set")
	}
	if found.Value == "" {
		t.Error("session cookie has an empty value")
	}
}

// TestSessionCookieSecureOption checks ServerOptions.CookieSecure actually
// changes the cookie's Secure attribute.
func TestSessionCookieSecureOption(t *testing.T) {
	ts, _ := newServer(t, agentboard.ServerOptions{CookieSecure: true})
	resp, body := do(t, http.MethodPost, ts.URL+"/api/auth/register",
		`{"email":"secure@example.com","password":"a fine password"}`, jsonContentType)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register status %d: %s", resp.StatusCode, body)
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == "agentboard_session" && !ck.Secure {
			t.Fatal("CookieSecure: true did not set Secure on the cookie")
		}
	}
}

// TestLogoutInvalidatesSessionServerSide checks that logging out really
// ends the session on the server, not just tells the browser to forget the
// cookie: replaying the exact same cookie value after logout must not work
// (this is the difference between a real server-side session and a
// stateless signed token, which cannot be revoked before it expires).
func TestLogoutInvalidatesSessionServerSide(t *testing.T) {
	ts, _ := newServer(t, agentboard.ServerOptions{})
	resp, _ := do(t, http.MethodPost, ts.URL+"/api/auth/register",
		`{"email":"replay@example.com","password":"a fine password"}`, jsonContentType)
	var sessionCookie string
	for _, ck := range resp.Cookies() {
		if ck.Name == "agentboard_session" {
			sessionCookie = cookieHeader(ck)
		}
	}
	resp.Body.Close()
	if sessionCookie == "" {
		t.Fatal("no session cookie from register")
	}

	logoutResp, _ := do(t, http.MethodPost, ts.URL+"/api/auth/logout", "{}", withCookie(jsonContentType, sessionCookie))
	logoutResp.Body.Close()

	meResp, meBody := do(t, http.MethodGet, ts.URL+"/api/auth/me", "", map[string]string{"Cookie": sessionCookie})
	defer meResp.Body.Close()
	if meResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replayed a logged-out session cookie: status %d body %s", meResp.StatusCode, meBody)
	}
}

// TestLoginRateLimiting checks that repeated failed logins for one account
// are eventually throttled (429), and that they do not lock out a
// *different* account (the limiter is keyed per email, not global).
func TestLoginRateLimiting(t *testing.T) {
	ts, _ := newServer(t, agentboard.ServerOptions{})
	c := agentboard.NewClient(ts.URL, "")
	ctx := context.Background()
	if _, err := c.Register(ctx, "limited@example.com", "the real password"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := c.Logout(ctx); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	var lastStatus int
	for i := 0; i < 20; i++ {
		_, err := c.Login(ctx, "limited@example.com", "wrong password")
		lastStatus = apiStatus(err)
		if lastStatus == http.StatusTooManyRequests {
			break
		}
	}
	if lastStatus != http.StatusTooManyRequests {
		t.Fatalf("20 failed logins never triggered rate limiting; last status %d", lastStatus)
	}

	// A different, unrelated account must be unaffected.
	if _, err := c.Register(ctx, "other@example.com", "another password"); err != nil {
		t.Fatalf("Register other account: %v", err)
	}
	if err := c.Logout(ctx); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := c.Login(ctx, "other@example.com", "another password"); err != nil {
		t.Fatalf("unrelated account got rate limited too: %v", err)
	}
}

var jsonContentType = map[string]string{"Content-Type": "application/json"}

// cookieHeader renders ck the way a browser would send it back in a request
// Cookie header ("name=value" only): http.Cookie.String() instead renders
// Set-Cookie-style output including attributes like Path and HttpOnly,
// which are meaningless (and invalid) in a request header.
func cookieHeader(ck *http.Cookie) string { return ck.Name + "=" + ck.Value }

func withCookie(hdr map[string]string, cookie string) map[string]string {
	out := map[string]string{"Cookie": cookie}
	for k, v := range hdr {
		out[k] = v
	}
	return out
}

// TestMeJSONShape pins the response shape of GET /api/auth/me so a future
// change notices if it accidentally starts leaking credential fields.
func TestMeJSONShape(t *testing.T) {
	ts, _ := newServer(t, agentboard.ServerOptions{})
	resp, _ := do(t, http.MethodPost, ts.URL+"/api/auth/register",
		`{"email":"shape@example.com","password":"a fine password"}`, jsonContentType)
	resp.Body.Close()
	var cookie string
	for _, ck := range resp.Cookies() {
		if ck.Name == "agentboard_session" {
			cookie = cookieHeader(ck)
		}
	}

	meResp, meBody := do(t, http.MethodGet, ts.URL+"/api/auth/me", "", map[string]string{"Cookie": cookie})
	defer meResp.Body.Close()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(meBody), &raw); err != nil {
		t.Fatalf("decode: %v (%s)", err, meBody)
	}
	for _, forbidden := range []string{"password_hash", "salt", "iterations", "password"} {
		if _, ok := raw[forbidden]; ok {
			t.Fatalf("/api/auth/me response contains %q: %s", forbidden, meBody)
		}
	}
	if _, ok := raw["email"]; !ok {
		t.Fatalf("/api/auth/me response is missing email: %s", meBody)
	}
}
