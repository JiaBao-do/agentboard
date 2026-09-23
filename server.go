package agentboard

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JiaBao-do/agentboard/internal/webui"
)

const maxBody = 1 << 20

// ServerOptions configures NewServer.
type ServerOptions struct {
	// Board is the board to serve. Required.
	Board *Board
	// Token, if set, is required as "Authorization: Bearer <token>" on all
	// /api/ requests. The event stream also accepts ?token= because the
	// browser EventSource cannot set headers.
	Token string
	// AllowedHosts, if non-empty, restricts the Host header (port ignored).
	// It defends a loopback-only server against DNS rebinding.
	AllowedHosts []string
	// Static is the web UI. Default: the embedded WebAssembly UI.
	Static fs.FS
	// Logger receives request errors. Default: slog.Default().
	Logger *slog.Logger
	// EnableShutdown turns on POST /api/admin/shutdown, which stops the
	// server gracefully. It is off by default. Serve refuses to run with it
	// on a non-loopback listener unless Token is set.
	EnableShutdown bool
	// KeepAlive is the interval of SSE "tick" events, which also make UIs
	// refresh presence. Default: 15 seconds.
	KeepAlive time.Duration
	// SessionTTL is how long a logged-in session stays valid after its last
	// use (a sliding window: every validated request extends it). Default:
	// 24 hours. Sessions are held in memory only (see docs/PITFALLS.md): a
	// restart logs everyone out.
	SessionTTL time.Duration
	// CookieSecure sets the Secure attribute on the session cookie, which
	// tells the browser to send it only over HTTPS. Leave it false for the
	// default plain-HTTP loopback setup; set it true only when a
	// TLS-terminating reverse proxy sits in front of agentboard (see
	// docs/PITFALLS.md - agentboard itself never speaks TLS). A true value
	// on a plain HTTP origin makes the browser silently refuse to ever send
	// the cookie, so get this right for your deployment.
	CookieSecure bool
}

// Server serves the REST API, the event stream and the embedded UI.
type Server struct {
	o        ServerOptions
	mux      *http.ServeMux
	static   fs.FS
	hosts    map[string]bool
	sessions *sessionStore
	logins   *loginLimiter

	gzMu sync.Mutex
	gz   map[string][]byte

	done     chan struct{} // closed when the server is asked to stop
	stopOnce sync.Once
}

// NewServer builds a Server. It panics if o.Board is nil.
func NewServer(o ServerOptions) *Server {
	if o.Board == nil {
		panic("agentboard: ServerOptions.Board is required")
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.KeepAlive <= 0 {
		o.KeepAlive = 15 * time.Second
	}
	s := &Server{
		o: o, mux: http.NewServeMux(), static: o.Static, gz: map[string][]byte{}, done: make(chan struct{}),
		sessions: newSessionStore(o.SessionTTL, nil), logins: newLoginLimiter(nil),
	}
	if s.static == nil {
		s.static = webui.FS()
	}
	if len(o.AllowedHosts) > 0 {
		s.hosts = map[string]bool{}
		for _, h := range o.AllowedHosts {
			s.hosts[strings.ToLower(h)] = true
		}
	}
	s.routes()
	return s
}

// Handler returns the http.Handler for the whole server.
func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.serveHTTP) }

// stop asks Serve to shut down; it is safe to call more than once.
func (s *Server) stop() { s.stopOnce.Do(func() { close(s.done) }) }

// Serve serves on ln until ctx is cancelled or an authorised shutdown request
// arrives. It sweeps expired leases every few seconds. On the way out it
// stops accepting connections, lets in-flight requests finish (up to five
// seconds), then records a final activity entry, marks agents offline and
// saves the board. A Server can be served once.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	if s.o.EnableShutdown && s.o.Token == "" && !isLoopback(ln.Addr()) {
		return errors.New("agentboard: refusing to enable shutdown on a non-loopback listener without a token")
	}
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	sweepDone := make(chan struct{})
	go func() {
		defer close(sweepDone)
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-s.done:
				return
			case <-t.C:
				s.o.Board.Sweep()
				s.sessions.sweep()
			}
		}
	}()
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		s.stop()
		<-sweepDone
		return err
	case <-ctx.Done():
	case <-s.done:
	}
	s.stop() // ends event streams so Shutdown does not wait on them
	<-sweepDone
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		_ = srv.Close()
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return s.o.Board.Shutdown("system")
}

func isLoopback(a net.Addr) bool {
	if ta, ok := a.(*net.TCPAddr); ok {
		return ta.IP.IsLoopback()
	}
	return false
}

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	m.HandleFunc("GET /api/state", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, s.o.Board.Snapshot())
	})
	m.HandleFunc("GET /api/projects", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, s.o.Board.Projects())
	})
	m.HandleFunc("POST /api/projects", func(w http.ResponseWriter, r *http.Request) {
		req, ok := decode[ProjectRequest](w, r)
		if !ok {
			return
		}
		p, err := s.o.Board.CreateProject(req.Key, req.Name, headerActor(r, req.Actor))
		s.reply(w, http.StatusCreated, p, err)
	})
	m.HandleFunc("GET /api/tasks", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		writeJSON(w, http.StatusOK, s.o.Board.Tasks(Filter{
			Project: q.Get("project"), Status: Status(q.Get("status")), Assignee: q.Get("assignee"),
			Type: Kind(q.Get("type")), Parent: q.Get("parent"),
		}))
	})
	m.HandleFunc("POST /api/tasks", func(w http.ResponseWriter, r *http.Request) {
		req, ok := decode[NewTask](w, r)
		if !ok {
			return
		}
		req.Actor = headerActor(r, req.Actor)
		t, err := s.o.Board.AddTask(req)
		s.reply(w, http.StatusCreated, t, err)
	})
	m.HandleFunc("GET /api/tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		d, err := s.o.Board.Task(r.PathValue("id"))
		s.reply(w, http.StatusOK, d, err)
	})
	m.HandleFunc("PATCH /api/tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		req, ok := decode[Patch](w, r)
		if !ok {
			return
		}
		req.Actor = headerActor(r, req.Actor)
		t, err := s.o.Board.Update(r.PathValue("id"), req)
		s.reply(w, http.StatusOK, t, err)
	})
	m.HandleFunc("POST /api/tasks/{id}/claim", func(w http.ResponseWriter, r *http.Request) {
		req, ok := decode[ClaimRequest](w, r)
		if !ok {
			return
		}
		t, err := s.o.Board.Claim(r.PathValue("id"), headerActor(r, req.Agent), time.Duration(req.LeaseSeconds)*time.Second)
		s.reply(w, http.StatusOK, t, err)
	})
	m.HandleFunc("POST /api/tasks/{id}/release", func(w http.ResponseWriter, r *http.Request) {
		req, ok := decode[AgentRequest](w, r)
		if !ok {
			return
		}
		t, err := s.o.Board.Release(r.PathValue("id"), headerActor(r, req.Agent))
		s.reply(w, http.StatusOK, t, err)
	})
	m.HandleFunc("POST /api/tasks/{id}/done", func(w http.ResponseWriter, r *http.Request) {
		req, ok := decode[AgentRequest](w, r)
		if !ok {
			return
		}
		t, err := s.o.Board.Complete(r.PathValue("id"), headerActor(r, req.Agent))
		s.reply(w, http.StatusOK, t, err)
	})
	m.HandleFunc("POST /api/tasks/{id}/comment", func(w http.ResponseWriter, r *http.Request) {
		req, ok := decode[CommentRequest](w, r)
		if !ok {
			return
		}
		err := s.o.Board.Comment(r.PathValue("id"), headerActor(r, req.Actor), req.Text)
		s.reply(w, http.StatusCreated, map[string]string{"status": "ok"}, err)
	})
	m.HandleFunc("GET /api/agents", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, s.o.Board.Agents())
	})
	m.HandleFunc("POST /api/agents/{name}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		req, ok := decode[HeartbeatRequest](w, r)
		if !ok {
			return
		}
		a, err := s.o.Board.Heartbeat(r.PathValue("name"), req)
		s.reply(w, http.StatusOK, a, err)
	})
	m.HandleFunc("GET /api/activity", func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit")) // invalid: 0, meaning the default
		writeJSON(w, http.StatusOK, s.o.Board.Recent(limit))
	})
	m.HandleFunc("GET /api/export", func(w http.ResponseWriter, _ *http.Request) {
		// Board.Export (never ExportWithCredentials) is deliberate: this
		// endpoint is network-reachable, so it must never carry a User's
		// PasswordHash/Salt/Iterations - see Export's doc comment and
		// docs/PITFALLS.md #12.
		st, err := s.o.Board.Export()
		if err != nil {
			s.reply(w, http.StatusOK, nil, err)
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="agentboard-export.json"`)
		writeJSON(w, http.StatusOK, st)
	})
	m.HandleFunc("POST /api/auth/register", s.handleRegister)
	m.HandleFunc("POST /api/auth/login", s.handleLogin)
	m.HandleFunc("POST /api/auth/logout", s.handleLogout)
	m.HandleFunc("GET /api/auth/me", s.handleMe)
	m.HandleFunc("/api/admin/shutdown", s.shutdown)
	m.HandleFunc("GET /api/events", s.events)
	m.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, apiError{Error: "no such endpoint"})
	})
	m.HandleFunc("/", s.serveStatic)
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy",
		"default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	if s.hosts != nil && !s.hosts[strings.ToLower(hostOnly(r.Host))] {
		writeJSON(w, http.StatusForbidden, apiError{Error: "host not allowed"})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") && !s.authorized(r) {
		h.Set("WWW-Authenticate", `Bearer realm="agentboard"`)
		writeJSON(w, http.StatusUnauthorized, apiError{Error: "missing or invalid token"})
		return
	}
	if r.Method == http.MethodPost || r.Method == http.MethodPatch {
		// Requiring JSON forces a CORS preflight for cross-site pages, which
		// closes the door on form-based CSRF against an unauthenticated
		// loopback server.
		if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
			writeJSON(w, http.StatusUnsupportedMediaType, apiError{Error: "Content-Type must be application/json"})
			return
		}
	}
	s.mux.ServeHTTP(w, r)
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return strings.Trim(hostport, "[]")
}

func (s *Server) authorized(r *http.Request) bool {
	if s.o.Token == "" {
		return true
	}
	got := ""
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		got = h[len("Bearer "):]
	} else if r.Method == http.MethodGet && r.URL.Path == "/api/events" {
		got = r.URL.Query().Get("token")
	}
	a, b := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(s.o.Token))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

type apiError struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) reply(w http.ResponseWriter, okStatus int, v any, err error) {
	if err == nil {
		writeJSON(w, okStatus, v)
		return
	}
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, ErrInvalid):
		status = http.StatusBadRequest
	case errors.Is(err, ErrClaimed), errors.Is(err, ErrNotOwner), errors.Is(err, ErrExists):
		status = http.StatusConflict
	case errors.Is(err, ErrBadCredentials):
		status = http.StatusUnauthorized
	default:
		s.o.Logger.Error("agentboard: request failed", "err", err)
		err = errors.New("internal error")
	}
	writeJSON(w, status, apiError{Error: err.Error()})
}

// actorHeader is the header agents may use instead of a body field to say
// who they are.
const actorHeader = "X-Agent-Name"

func headerActor(r *http.Request, own string) string {
	if own != "" {
		return own
	}
	return r.Header.Get(actorHeader)
}

func decode[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var v T
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		status := http.StatusBadRequest
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			status = http.StatusRequestEntityTooLarge
		}
		writeJSON(w, status, apiError{Error: "invalid JSON body: " + err.Error()})
		return v, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid JSON body: trailing data"})
		return v, false
	}
	return v, true
}

// events streams change notifications as Server-Sent Events. Clients
// re-fetch /api/state on every event; payloads are only hints.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "streaming unsupported"})
		return
	}
	ch, cancel := s.o.Board.Subscribe()
	defer cancel()
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "retry: 3000\nevent: hello\ndata: {}\n\n")
	fl.Flush()
	tick := time.NewTicker(s.o.KeepAlive)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.done:
			fmt.Fprint(w, "event: bye\ndata: {}\n\n")
			fl.Flush()
			return
		case ev, open := <-ch:
			if !open {
				return
			}
			b, _ := json.Marshal(ev)
			fmt.Fprintf(w, "event: change\ndata: %s\n\n", b)
		case <-tick.C:
			fmt.Fprint(w, "event: tick\ndata: {}\n\n")
		}
		fl.Flush()
	}
}

func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeJSON(w, http.StatusMethodNotAllowed, apiError{Error: "use GET"})
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	data, err := fs.ReadFile(s.static, name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, apiError{Error: "not found"})
		return
	}
	ext := path.Ext(name)
	ctype := mime.TypeByExtension(ext)
	switch ext {
	case ".wasm":
		ctype = "application/wasm"
	case ".js":
		ctype = "text/javascript; charset=utf-8"
	}
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	sum := sha256.Sum256(data)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("ETag", etag)
	h.Set("Cache-Control", "no-cache")
	h.Add("Vary", "Accept-Encoding")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body := data
	if len(data) > 1024 && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		body = s.gzipped(name+etag, data)
		h.Set("Content-Encoding", "gzip")
	}
	h.Set("Content-Length", fmt.Sprint(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// gzipped compresses static assets once and caches the result; the cache key
// includes the content hash, so it never serves stale bytes.
func (s *Server) gzipped(key string, data []byte) []byte {
	s.gzMu.Lock()
	defer s.gzMu.Unlock()
	if b, ok := s.gz[key]; ok {
		return b
	}
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	_, _ = zw.Write(data)
	_ = zw.Close()
	s.gz[key] = buf.Bytes()
	return s.gz[key]
}

// shutdown handles POST /api/admin/shutdown. A page on the internet can make
// your browser send requests to a server on localhost, so on top of the
// bearer token (checked for every /api/ call) this endpoint demands: the POST
// method, a custom X-Agentboard-Action header (which forces a CORS preflight
// that this server never answers), an Origin that matches the Host when the
// browser sends one, and an allowed Host or a token, which defeats DNS
// rebinding.
func (s *Server) shutdown(w http.ResponseWriter, r *http.Request) {
	switch {
	case !s.o.EnableShutdown:
		writeJSON(w, http.StatusForbidden, apiError{Error: "shutdown is disabled on this server"})
		return
	case r.Method != http.MethodPost:
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, apiError{Error: "use POST"})
		return
	case r.Header.Get("X-Agentboard-Action") != "shutdown":
		writeJSON(w, http.StatusForbidden, apiError{Error: "missing X-Agentboard-Action: shutdown header"})
		return
	case s.hosts == nil && s.o.Token == "":
		writeJSON(w, http.StatusForbidden, apiError{Error: "shutdown needs allowed hosts or a token"})
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			writeJSON(w, http.StatusForbidden, apiError{Error: "cross-origin request refused"})
			return
		}
	}
	w.Header().Set("Connection", "close")
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "shutting down"})
	if fl, ok := w.(http.Flusher); ok {
		fl.Flush() // the client gets its 202 before the listener closes
	}
	s.stop()
}
