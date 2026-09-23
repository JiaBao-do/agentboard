package agentboard_test

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/JiaBao-do/agentboard"
)

func newServer(t *testing.T, so agentboard.ServerOptions) (*httptest.Server, *agentboard.Board) {
	t.Helper()
	b, err := agentboard.Open(agentboard.Options{SaveMode: agentboard.SaveSync})
	if err != nil {
		t.Fatal(err)
	}
	so.Board = b
	ts := httptest.NewServer(agentboard.NewServer(so).Handler())
	t.Cleanup(func() {
		ts.CloseClientConnections()
		ts.Close()
		_ = b.Close(context.Background())
	})
	return ts, b
}

func do(t *testing.T, method, url, body string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, string(data)
}

var jsonHdr = map[string]string{"Content-Type": "application/json", "X-Agent-Name": "tester"}

func TestAPIFlowThroughClient(t *testing.T) {
	ts, _ := newServer(t, agentboard.ServerOptions{})
	c := agentboard.NewClient(ts.URL+"/", "")
	c.Agent = "tester"
	ctx := context.Background()

	if _, err := c.CreateProject(ctx, agentboard.ProjectRequest{Key: "AB", Name: "Board"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateProject(ctx, agentboard.ProjectRequest{Key: "AB", Name: "Again"}); apiStatus(err) != http.StatusConflict {
		t.Fatalf("duplicate project: %v", err)
	}
	epic, err := c.AddTask(ctx, agentboard.NewTask{Actor: "tester", Project: "AB", Type: agentboard.KindEpic, Title: "Epic"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := c.AddTask(ctx, agentboard.NewTask{Actor: "tester", Project: "AB", Title: "Do it", Parent: epic.ID, Labels: []string{"go"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddTask(ctx, agentboard.NewTask{Actor: "tester", Project: "AB", Title: ""}); apiStatus(err) != http.StatusBadRequest {
		t.Fatalf("empty title: %v", err)
	}
	if _, err := c.Task(ctx, "AB-99"); apiStatus(err) != http.StatusNotFound {
		t.Fatalf("missing task: %v", err)
	}

	if _, err := c.Claim(ctx, task.ID, "alice", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Claim(ctx, task.ID, "bob", time.Minute); apiStatus(err) != http.StatusConflict {
		t.Fatalf("second claim: %v", err)
	}
	if _, err := c.Release(ctx, task.ID, "bob"); apiStatus(err) != http.StatusConflict {
		t.Fatalf("release by non-owner: %v", err)
	}
	a, err := c.Heartbeat(ctx, "alice", agentboard.HeartbeatRequest{Kind: "worker", Task: task.ID, Meta: map[string]string{"host": "h"}})
	if err != nil || !a.Online || a.Meta["host"] != "h" {
		t.Fatalf("heartbeat: %+v %v", a, err)
	}
	status := agentboard.StatusReview
	if _, err := c.Update(ctx, task.ID, agentboard.Patch{Status: &status, Actor: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Comment(ctx, task.ID, "alice", "looks good"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Done(ctx, task.ID, "alice"); err != nil {
		t.Fatal(err)
	}

	d, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, e := range d.Activity {
		actions = append(actions, e.Action)
	}
	if got := strings.Join(actions, ","); got != "created,claimed,status,comment,done" {
		t.Fatalf("timeline = %s", got)
	}
	if d.Task.Parent != epic.ID || d.Task.Status != agentboard.StatusDone {
		t.Fatalf("task = %+v", d.Task)
	}

	kids, err := c.Tasks(ctx, agentboard.Filter{Parent: epic.ID, Type: agentboard.KindTask, Status: agentboard.StatusDone, Assignee: "alice", Project: "AB"})
	if err != nil || len(kids) != 1 {
		t.Fatalf("filtered tasks = %+v err=%v", kids, err)
	}
	snap, err := c.State(ctx)
	if err != nil || len(snap.Tasks) != 2 || len(snap.Agents) != 1 || snap.Save.Mode != "sync" || snap.Now.IsZero() {
		t.Fatalf("state = %+v err=%v", snap, err)
	}
	st, err := c.Export(ctx)
	if err != nil || st.Version != agentboard.SchemaVersion || len(st.Tasks) != 2 {
		t.Fatalf("export = %+v err=%v", st, err)
	}
	if err := agentboard.ValidateState(st); err != nil {
		t.Fatalf("exported state invalid: %v", err)
	}
}

func apiStatus(err error) int {
	var ae *agentboard.APIError
	if errors.As(err, &ae) {
		return ae.Status
	}
	return 0
}

func TestAuth(t *testing.T) {
	ts, _ := newServer(t, agentboard.ServerOptions{Token: "s3cret"})
	tests := []struct {
		name   string
		method string
		path   string
		hdr    map[string]string
		want   int
	}{
		{"no token", "GET", "/api/state", nil, 401},
		{"wrong token", "GET", "/api/state", map[string]string{"Authorization": "Bearer nope"}, 401},
		{"wrong scheme", "GET", "/api/state", map[string]string{"Authorization": "Basic s3cret"}, 401},
		{"right token", "GET", "/api/state", map[string]string{"Authorization": "Bearer s3cret"}, 200},
		{"query token rejected on normal endpoints", "GET", "/api/state?token=s3cret", nil, 401},
		{"static assets stay open", "GET", "/", nil, 200},
		{"health stays open", "GET", "/healthz", nil, 200},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, _ := do(t, tc.method, ts.URL+tc.path, "", tc.hdr)
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.want)
			}
			if tc.want == 401 && resp.Header.Get("WWW-Authenticate") == "" {
				t.Error("401 without WWW-Authenticate")
			}
		})
	}
	t.Run("event stream accepts the query token", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/api/events?token=s3cret")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
			t.Fatalf("status=%d type=%s", resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		resp2, _ := do(t, "GET", ts.URL+"/api/events?token=wrong", "", nil)
		if resp2.StatusCode != 401 {
			t.Fatalf("wrong query token: %d", resp2.StatusCode)
		}
	})
}

func TestHostAllowList(t *testing.T) {
	ts, _ := newServer(t, agentboard.ServerOptions{AllowedHosts: []string{"127.0.0.1", "localhost"}})
	req, _ := http.NewRequest("GET", ts.URL+"/api/state", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("own host refused: %v %v", resp, err)
	}
	resp.Body.Close()
	for _, host := range []string{"evil.example", "evil.example:80", "127.0.0.1.evil.example"} {
		req, _ := http.NewRequest("GET", ts.URL+"/api/state", nil)
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 403 {
			t.Errorf("Host %q: status %d, want 403 (DNS rebinding)", host, resp.StatusCode)
		}
	}
}

func TestRequestValidation(t *testing.T) {
	ts, _ := newServer(t, agentboard.ServerOptions{})
	do(t, "POST", ts.URL+"/api/projects", `{"key":"AB","name":"n"}`, jsonHdr)
	tests := []struct {
		name, method, path, body string
		hdr                      map[string]string
		want                     int
	}{
		{"form post is refused (CSRF)", "POST", "/api/tasks", "project=AB&title=x", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, 415},
		{"text/plain post is refused", "POST", "/api/tasks", `{"project":"AB","title":"x"}`, map[string]string{"Content-Type": "text/plain"}, 415},
		{"no content type", "POST", "/api/tasks", `{"project":"AB","title":"x"}`, nil, 415},
		{"json with charset ok", "POST", "/api/tasks", `{"project":"AB","title":"x"}`, map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Agent-Name": "tester"}, 201},
		{"anonymous write is refused", "POST", "/api/tasks", `{"project":"AB","title":"x"}`, map[string]string{"Content-Type": "application/json"}, 400},
		{"body actor works without the header", "POST", "/api/tasks", `{"project":"AB","title":"x","actor":"alice"}`, map[string]string{"Content-Type": "application/json"}, 201},
		{"system actor is refused", "POST", "/api/tasks", `{"project":"AB","title":"x","actor":"system"}`, map[string]string{"Content-Type": "application/json"}, 400},
		{"unknown field", "POST", "/api/tasks", `{"project":"AB","title":"x","typo":1}`, jsonHdr, 400},
		{"broken json", "POST", "/api/tasks", `{"project":`, jsonHdr, 400},
		{"trailing data", "POST", "/api/tasks", `{"project":"AB","title":"x"} {}`, jsonHdr, 400},
		{"too large", "POST", "/api/tasks", `{"project":"AB","title":"x","description":"` + strings.Repeat("a", 1<<20) + `"}`, jsonHdr, 413},
		{"unknown endpoint", "GET", "/api/nope", "", nil, 404},
		{"unknown static", "GET", "/nope.txt", "", nil, 404},
		{"path traversal", "GET", "/../go.mod", "", nil, 404},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := do(t, tc.method, ts.URL+tc.path, tc.body, tc.hdr)
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d (%s)", resp.StatusCode, tc.want, body)
			}
			if tc.want >= 400 && !strings.Contains(resp.Header.Get("Content-Type"), "json") {
				t.Errorf("error response is not JSON: %s", resp.Header.Get("Content-Type"))
			}
		})
	}
}

func TestSecurityHeaders(t *testing.T) {
	ts, _ := newServer(t, agentboard.ServerOptions{})
	resp, _ := do(t, "GET", ts.URL+"/", "", nil)
	csp := resp.Header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "'wasm-unsafe-eval'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "http") {
		t.Errorf("CSP must not allow inline code or remote origins: %q", csp)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("X-Frame-Options") != "DENY" {
		t.Errorf("missing hardening headers: %v", resp.Header)
	}
}

func TestStaticAssets(t *testing.T) {
	wasm := bytes.Repeat([]byte("\x00asm"), 4096)
	ts, _ := newServer(t, agentboard.ServerOptions{Static: fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><title>x</title>")},
		"app.wasm":   {Data: wasm},
		"boot.js":    {Data: []byte("//js")},
	}})

	resp, body := do(t, "GET", ts.URL+"/", "", nil)
	if resp.StatusCode != 200 || !strings.Contains(body, "<title>x</title>") || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("index: %d %q %s", resp.StatusCode, body, resp.Header.Get("Content-Type"))
	}
	resp, _ = do(t, "GET", ts.URL+"/boot.js", "", nil)
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/javascript") {
		t.Fatalf("js type = %s", resp.Header.Get("Content-Type"))
	}

	// wasm needs application/wasm for streaming instantiation, and is
	// served compressed only to clients that ask for it.
	req, _ := http.NewRequest("GET", ts.URL+"/app.wasm", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	raw := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	resp, err := raw.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "application/wasm" || resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("headers = %v", resp.Header)
	}
	zr, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(zr)
	if !bytes.Equal(got, wasm) {
		t.Fatal("gzip round trip changed the bytes")
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	req2, _ := http.NewRequest("GET", ts.URL+"/app.wasm", nil)
	req2.Header.Set("If-None-Match", etag)
	resp2, err := raw.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotModified {
		t.Fatalf("conditional request = %d", resp2.StatusCode)
	}
	resp3, _ := do(t, "GET", ts.URL+"/app.wasm", "", nil)
	if resp3.Header.Get("Content-Encoding") != "" && resp3.Header.Get("Accept-Encoding") == "" {
		// The default client asks for gzip and decodes it transparently, so
		// only check the uncompressed path with DisableCompression.
		t.Log("default client negotiated gzip")
	}
}

func TestEmbeddedUIIsServed(t *testing.T) {
	ts, _ := newServer(t, agentboard.ServerOptions{})
	for path, ctype := range map[string]string{
		"/":             "text/html",
		"/index.html":   "text/html",
		"/style.css":    "text/css",
		"/boot.js":      "text/javascript",
		"/wasm_exec.js": "text/javascript",
		"/app.wasm":     "application/wasm",
	} {
		resp, body := do(t, "GET", ts.URL+path, "", nil)
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), ctype) || len(body) == 0 {
			t.Errorf("%s: status %d type %q len %d", path, resp.StatusCode, resp.Header.Get("Content-Type"), len(body))
		}
	}
	_, wasm := do(t, "GET", ts.URL+"/app.wasm", "", nil)
	if !strings.HasPrefix(wasm, "\x00asm") {
		t.Error("app.wasm is not a WebAssembly module")
	}
}

func TestEventStream(t *testing.T) {
	ts, b := newServer(t, agentboard.ServerOptions{KeepAlive: 20 * time.Millisecond})
	resp, err := http.Get(ts.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	seen := map[string]bool{}
	go func() {
		time.Sleep(50 * time.Millisecond)
		b.CreateProject("AB", "n", "tester")
	}()
	deadline := time.AfterFunc(10*time.Second, func() { resp.Body.Close() })
	defer deadline.Stop()
	for sc.Scan() {
		if name, ok := strings.CutPrefix(sc.Text(), "event: "); ok {
			seen[name] = true
		}
		if strings.HasPrefix(sc.Text(), "data: ") && strings.Contains(sc.Text(), `"type":"project"`) {
			seen["project-data"] = true
		}
		if seen["hello"] && seen["change"] && seen["tick"] && seen["project-data"] {
			return
		}
	}
	t.Fatalf("event stream missed events, saw %v", seen)
}

// --- shutdown ---------------------------------------------------------------

func shutdownReq(t *testing.T, ts *httptest.Server, method string, hdr map[string]string) *http.Response {
	t.Helper()
	resp, _ := do(t, method, ts.URL+"/api/admin/shutdown", "{}", hdr)
	return resp
}

func TestShutdownEndpointRejections(t *testing.T) {
	good := map[string]string{"Content-Type": "application/json", "X-Agentboard-Action": "shutdown"}
	with := func(k, v string) map[string]string {
		m := map[string]string{}
		for kk, vv := range good {
			m[kk] = vv
		}
		if v == "" {
			delete(m, k)
		} else {
			m[k] = v
		}
		return m
	}
	hosts := []string{"127.0.0.1"}
	tests := []struct {
		name string
		opts agentboard.ServerOptions
		meth string
		hdr  map[string]string
		want int
	}{
		{"disabled by default", agentboard.ServerOptions{AllowedHosts: hosts}, "POST", good, 403},
		{"GET is never allowed", agentboard.ServerOptions{EnableShutdown: true, AllowedHosts: hosts}, "GET", good, 405},
		{"missing action header", agentboard.ServerOptions{EnableShutdown: true, AllowedHosts: hosts}, "POST", with("X-Agentboard-Action", ""), 403},
		{"wrong action header", agentboard.ServerOptions{EnableShutdown: true, AllowedHosts: hosts}, "POST", with("X-Agentboard-Action", "restart"), 403},
		{"cross-origin page", agentboard.ServerOptions{EnableShutdown: true, AllowedHosts: hosts}, "POST", with("Origin", "http://evil.example"), 403},
		{"cross-origin same host other port", agentboard.ServerOptions{EnableShutdown: true, AllowedHosts: hosts}, "POST", with("Origin", "http://127.0.0.1:1"), 403},
		{"no host list and no token", agentboard.ServerOptions{EnableShutdown: true}, "POST", good, 403},
		{"token required when set", agentboard.ServerOptions{EnableShutdown: true, Token: "t"}, "POST", good, 401},
		{"wrong token", agentboard.ServerOptions{EnableShutdown: true, Token: "t"}, "POST", with("Authorization", "Bearer x"), 401},
		{"form content type", agentboard.ServerOptions{EnableShutdown: true, AllowedHosts: hosts}, "POST", with("Content-Type", "application/x-www-form-urlencoded"), 415},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts, _ := newServer(t, tc.opts)
			resp := shutdownReq(t, ts, tc.meth, tc.hdr)
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.want)
			}
			// The server must still be up.
			if r, _ := do(t, "GET", ts.URL+"/healthz", "", map[string]string{}); r.StatusCode != 200 {
				t.Fatalf("server stopped despite the refusal (%d)", r.StatusCode)
			}
		})
	}
	t.Run("DNS rebinding host", func(t *testing.T) {
		ts, _ := newServer(t, agentboard.ServerOptions{EnableShutdown: true, AllowedHosts: hosts})
		req, _ := http.NewRequest("POST", ts.URL+"/api/admin/shutdown", strings.NewReader("{}"))
		for k, v := range good {
			req.Header.Set(k, v)
		}
		req.Host = "evil.example"
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 403 {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	})
}

func serveOn(t *testing.T, b *agentboard.Board, so agentboard.ServerOptions) (base string, done <-chan error, cancel context.CancelFunc) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	so.Board = b
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	errc := make(chan error, 1)
	go func() { errc <- agentboard.NewServer(so).Serve(ctx, ln) }()
	return "http://" + ln.Addr().String(), errc, cancel
}

func TestShutdownHappyPathKeepsDataAndReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.json")
	b, err := agentboard.Open(agentboard.Options{Store: agentboard.NewFileStore(path), SaveDebounce: time.Hour, SaveMaxLatency: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	base, done, _ := serveOn(t, b, agentboard.ServerOptions{EnableShutdown: true, AllowedHosts: []string{"127.0.0.1"}})
	c := agentboard.NewClient(base, "")
	c.Agent = "tester"
	ctx := context.Background()
	c.CreateProject(ctx, agentboard.ProjectRequest{Key: "AB", Name: "n"})
	task, _ := c.AddTask(ctx, agentboard.NewTask{Actor: "tester", Project: "AB", Title: "survive"})
	c.Claim(ctx, task.ID, "alice", time.Hour)

	// An in-flight request: an open event stream must be ended politely, not
	// cut off.
	resp, err := http.Get(base + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	streamEnded := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(resp.Body)
		streamEnded <- string(data)
	}()

	if err := c.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown request: %v", err) // the 202 must arrive before the listener closes
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server did not stop")
	}
	if body := <-streamEnded; !strings.Contains(body, "event: bye") {
		t.Fatalf("in-flight stream was not closed gracefully: %q", body)
	}
	if _, err := http.Get(base + "/healthz"); err == nil {
		t.Fatal("listener still open after shutdown")
	}

	// The unsaved write-behind window was flushed: everything is on disk.
	b2, err := agentboard.Open(agentboard.Options{Store: agentboard.NewFileStore(path), SaveMode: agentboard.SaveSync})
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close(ctx)
	d, err := b2.Task(task.ID)
	if err != nil || d.Task.Assignee != "alice" {
		t.Fatalf("task after restart = %+v err=%v", d, err)
	}
	if last := b2.Recent(1)[0]; last.Action != "server_stopped" {
		t.Fatalf("last activity = %+v", last)
	}
	if b2.Agents()[0].Online {
		t.Fatal("agents must be offline after a shutdown")
	}
}

func TestServeStopsOnContextCancel(t *testing.T) {
	b, _ := agentboard.Open(agentboard.Options{})
	base, done, cancel := serveOn(t, b, agentboard.ServerOptions{})
	if resp, err := http.Get(base + "/healthz"); err != nil || resp.StatusCode != 200 {
		t.Fatalf("healthz: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
}

func TestServeRefusesShutdownOnPublicListenerWithoutToken(t *testing.T) {
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Skip("cannot listen on all interfaces here")
	}
	defer ln.Close()
	b, _ := agentboard.Open(agentboard.Options{})
	err = agentboard.NewServer(agentboard.ServerOptions{Board: b, EnableShutdown: true}).Serve(context.Background(), ln)
	if err == nil || !strings.Contains(err.Error(), "non-loopback") {
		t.Fatalf("err = %v", err)
	}
	// With a token it is allowed.
	ctx, cancel := context.WithCancel(context.Background())
	ln2, _ := net.Listen("tcp", "0.0.0.0:0")
	errc := make(chan error, 1)
	go func() {
		errc <- agentboard.NewServer(agentboard.ServerOptions{Board: b, EnableShutdown: true, Token: "x"}).Serve(ctx, ln2)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-errc; err != nil {
		t.Fatalf("with token: %v", err)
	}
}

// --- webhook and client details ----------------------------------------------

func TestWebhookDeliversSignedEvents(t *testing.T) {
	type hit struct {
		body []byte
		sig  string
		evt  string
	}
	hits := make(chan hit, 8)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		hits <- hit{body, r.Header.Get("X-Agentboard-Signature"), r.Header.Get("X-Agentboard-Event")}
	}))
	defer recv.Close()

	b, _ := agentboard.Open(agentboard.Options{SaveMode: agentboard.SaveSync})
	defer b.Close(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wh := &agentboard.Webhook{URL: recv.URL, Secret: "topsecret"}
	wait := wh.Start(ctx, b) // subscribed before Start returns: nothing below can be missed

	b.CreateProject("AB", "n", "tester")
	var first hit
	select {
	case first = <-hits:
	case <-time.After(10 * time.Second):
		t.Fatal("no webhook delivery")
	}
	if first.evt != "project" {
		t.Fatalf("event header = %q", first.evt)
	}
	task, _ := b.AddTask(agentboard.NewTask{Actor: "tester", Project: "AB", Title: "hooked"})
	var h hit
	select {
	case h = <-hits:
	case <-time.After(10 * time.Second):
		t.Fatal("no task delivery")
	}
	var p agentboard.WebhookPayload
	if err := json.Unmarshal(h.body, &p); err != nil || p.Event.Type != "task" || p.Task == nil || p.Task.ID != task.ID {
		t.Fatalf("payload = %s err=%v", h.body, err)
	}
	if want := agentboard.SignWebhookBody("topsecret", h.body); h.sig != want {
		t.Fatalf("signature = %q, want %q", h.sig, want)
	}
	cancel()
	wait()
}

func TestWebhookWithoutSecretIsUnsignedAndFailuresDoNotBlock(t *testing.T) {
	called := make(chan struct{}, 64)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Agentboard-Signature") != "" {
			t.Error("unsigned webhook carried a signature")
		}
		select {
		case called <- struct{}{}:
		default:
		}
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer recv.Close()
	b, _ := agentboard.Open(agentboard.Options{SaveMode: agentboard.SaveSync})
	defer b.Close(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	wait := (&agentboard.Webhook{URL: recv.URL}).Start(ctx, b)

	started := time.Now()
	for i := range 20 { // a failing endpoint must never slow the board down
		b.CreateProject("P"+string(rune('A'+i)), "n", "tester")
	}
	if took := time.Since(started); took > 5*time.Second {
		t.Errorf("20 changes took %v with a failing webhook", took)
	}
	select {
	case <-called:
	case <-time.After(10 * time.Second):
		t.Error("the failing endpoint was never called")
	}
	cancel()
	wait()
}

func TestWebhookSignatureVerification(t *testing.T) {
	body := []byte(`{"event":{"type":"task"}}`)
	sig := agentboard.SignWebhookBody("k", body)
	if !strings.HasPrefix(sig, "sha256=") || len(sig) != len("sha256=")+64 {
		t.Fatalf("signature = %q", sig)
	}
	if !agentboard.VerifyWebhookSignature("k", body, sig) {
		t.Fatal("valid signature rejected")
	}
	for name, tc := range map[string]struct {
		secret string
		body   []byte
		sig    string
	}{
		"wrong secret":   {"other", body, sig},
		"tampered body":  {"k", []byte(`{"event":{"type":"evil"}}`), sig},
		"empty header":   {"k", body, ""},
		"missing prefix": {"k", body, sig[len("sha256="):]},
	} {
		if agentboard.VerifyWebhookSignature(tc.secret, tc.body, tc.sig) {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestValidateWebhookURL(t *testing.T) {
	for raw, ok := range map[string]bool{
		"http://localhost:9000/hook": true,
		"https://example.com/x":      true,
		"ftp://example.com":          false,
		"example.com/hook":           false,
		"":                           false,
		"http://":                    false,
	} {
		if err := agentboard.ValidateWebhookURL(raw); (err == nil) != ok {
			t.Errorf("%q: err = %v, want ok=%v", raw, err, ok)
		}
	}
}

func TestClientErrorsAndRawMessages(t *testing.T) {
	raw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "plain text failure", http.StatusBadGateway)
	}))
	defer raw.Close()
	_, err := agentboard.NewClient(raw.URL, "").State(context.Background())
	var ae *agentboard.APIError
	if !errors.As(err, &ae) || ae.Status != 502 || !strings.Contains(ae.Message, "plain text failure") {
		t.Fatalf("err = %v", err)
	}
	if _, err := agentboard.NewClient("http://127.0.0.1:1", "").State(context.Background()); err == nil {
		t.Fatal("connection failure must be an error")
	}
	if err := agentboard.NewClient(raw.URL, "").Shutdown(context.Background()); apiStatus(err) != 502 {
		t.Fatalf("shutdown err = %v", err)
	}
}

func TestWritesAreAttributedToTheActingAgent(t *testing.T) {
	ts, _ := newServer(t, agentboard.ServerOptions{})
	post := func(path, body, agent string) *http.Response {
		hdr := map[string]string{"Content-Type": "application/json"}
		if agent != "" {
			hdr["X-Agent-Name"] = agent
		}
		resp, _ := do(t, "POST", ts.URL+path, body, hdr)
		return resp
	}
	if r := post("/api/projects", `{"key":"AB","name":"n"}`, ""); r.StatusCode != 400 {
		t.Fatalf("anonymous project = %d", r.StatusCode)
	}
	post("/api/projects", `{"key":"AB","name":"n"}`, "batchx-builder")
	post("/api/tasks", `{"project":"AB","title":"t"}`, "batchx-builder")
	post("/api/tasks/AB-1/claim", `{}`, "worker-1")
	post("/api/tasks/AB-1/comment", `{"text":"hi"}`, "worker-1")
	_, body := do(t, "GET", ts.URL+"/api/tasks/AB-1", "", nil)
	var d agentboard.TaskDetail
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatal(err)
	}
	if d.Task.CreatedBy != "batchx-builder" || d.Task.UpdatedBy != "worker-1" || d.Task.Assignee != "worker-1" {
		t.Fatalf("task = %+v", d.Task)
	}
	var actors []string
	for _, e := range d.Activity {
		actors = append(actors, e.Actor)
	}
	if got := strings.Join(actors, ","); got != "batchx-builder,worker-1,worker-1" {
		t.Fatalf("timeline actors = %s", got)
	}
}

func TestLeaseExpiryIsAttributedToTheSystem(t *testing.T) {
	b, c := newBoard(t)
	addTask(t, b, "job")
	b.Claim("AB-1", "alice", time.Minute)
	c.Advance(time.Minute)
	d, _ := b.Task("AB-1")
	if d.Task.UpdatedBy != "system" {
		t.Fatalf("UpdatedBy = %q", d.Task.UpdatedBy)
	}
}
