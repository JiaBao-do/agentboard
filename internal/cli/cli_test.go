package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/JiaBao-do/agentboard"
	"github.com/JiaBao-do/agentboard/internal/cli"
)

func noEnv(string) string { return "" }

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// run executes the CLI and returns its exit code, stdout and stderr.
func run(t *testing.T, env func(string) string, args ...string) (int, string, string) {
	t.Helper()
	var out, errw bytes.Buffer
	code := cli.Run(context.Background(), args, env, &out, &errw)
	return code, out.String(), errw.String()
}

func testServer(t *testing.T) (*httptest.Server, *agentboard.Board) {
	t.Helper()
	b, err := agentboard.Open(agentboard.Options{SaveMode: agentboard.SaveSync})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(agentboard.NewServer(agentboard.ServerOptions{Board: b}).Handler())
	t.Cleanup(func() { ts.Close(); _ = b.Close(context.Background()) })
	return ts, b
}

func TestUsageAndHelp(t *testing.T) {
	tests := []struct {
		name string
		args []string
		code int
		out  string // substring of stdout
		err  string // substring of stderr
	}{
		{"no args", nil, 2, "", "Usage:"},
		{"help", []string{"help"}, 0, "agentboard serve", ""},
		{"-h", []string{"--help"}, 0, "Usage:", ""},
		{"version", []string{"version"}, 0, "agentboard", ""},
		{"unknown command", []string{"frobnicate"}, 2, "", `unknown command "frobnicate"`},
		{"task without subcommand", []string{"task"}, 2, "", "usage: agentboard task"},
		{"unknown task command", []string{"task", "explode"}, 2, "", `unknown task command "explode"`},
		{"project usage", []string{"project"}, 2, "", "project add KEY NAME"},
		{"project missing name", []string{"project", "add", "AB"}, 2, "", "project add KEY NAME"},
		{"bad flag", []string{"status", "-nope"}, 2, "", "flag provided but not defined"},
		{"sub help is not an error", []string{"task", "list", "-h"}, 0, "", "Usage of task list"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errw := run(t, noEnv, tc.args...)
			if code != tc.code || !strings.Contains(out, tc.out) || !strings.Contains(errw, tc.err) {
				t.Fatalf("code=%d out=%q err=%q", code, out, errw)
			}
		})
	}
}

func TestClientFlowEndToEnd(t *testing.T) {
	ts, _ := testServer(t)
	env := envOf(map[string]string{"AGENTBOARD_URL": ts.URL, "AGENTBOARD_AGENT": "alice", "AGENTBOARD_PROJECT": "AB"})
	ok := func(args ...string) string {
		t.Helper()
		code, out, errw := run(t, env, args...)
		if code != 0 {
			t.Fatalf("%v: exit %d\nstdout: %s\nstderr: %s", args, code, out, errw)
		}
		return strings.TrimSpace(out)
	}

	if got := ok("project", "add", "AB", "Agent", "Board"); got != "AB" {
		t.Fatalf("project add = %q", got)
	}
	if got := ok("project", "add", "AB", "Again"); got != "AB (exists)" {
		t.Fatalf("second project add = %q, want idempotent", got)
	}
	epic := ok("task", "add", "-type", "epic", "Ship", "it")
	story := ok("task", "add", "-type", "story", "-parent", epic, "-priority", "high", "-labels", "go, cli", "Build the CLI")
	task := ok("task", "add", "-parent", story, "-desc", "with tests", "Write tests")
	if epic != "AB-1" || story != "AB-2" || task != "AB-3" {
		t.Fatalf("ids = %s %s %s", epic, story, task)
	}

	if got := ok("task", "claim", task, "-lease", "5m"); got != task {
		t.Fatalf("claim = %q", got)
	}
	ok("agent", "heartbeat", "-kind", "worker", "-task", task, "-meta", "host=h1", "-meta", "model=x")
	working := ok("status")
	for _, want := range []string{"in_progress 1", "alice", "worker", `working on AB-3 "Write tests"`} {
		if !strings.Contains(working, want) {
			t.Errorf("status lacks %q:\n%s", want, working)
		}
	}
	ok("task", "update", task, "-status", "review", "-priority", "urgent")
	ok("task", "comment", task, "ready", "for", "review")

	list := ok("task", "list", "-status", "review")
	if !strings.Contains(list, "AB-3") || !strings.Contains(list, "alice") || strings.Contains(list, "AB-2") {
		t.Fatalf("list =\n%s", list)
	}
	if kids := ok("task", "list", "-parent", story, "-type", "task"); !strings.Contains(kids, "Write tests") {
		t.Fatalf("children =\n%s", kids)
	}
	show := ok("task", "show", task)
	for _, want := range []string{"Write tests", "with tests", "agent alice", "parent AB-2", "timeline:", "ready for review", "claimed"} {
		if !strings.Contains(show, want) {
			t.Errorf("show lacks %q:\n%s", want, show)
		}
	}
	status := ok("status")
	// Moving the task out of in_progress ends the agent's current work.
	for _, want := range []string{"review 1", "todo 2", "alice", "worker", "idle"} {
		if !strings.Contains(status, want) {
			t.Errorf("status lacks %q:\n%s", want, status)
		}
	}

	ok("task", "release", task)
	ok("task", "claim", task)
	if got := ok("task", "done", task); got != task {
		t.Fatalf("done = %q", got)
	}

	var tasks []agentboard.Task
	if err := json.Unmarshal([]byte(ok("task", "list", "-json", "-status", "done")), &tasks); err != nil || len(tasks) != 1 || tasks[0].Assignee != "alice" {
		t.Fatalf("json list = %+v err=%v", tasks, err)
	}
	var snap agentboard.Snapshot
	if err := json.Unmarshal([]byte(ok("status", "-json")), &snap); err != nil || len(snap.Tasks) != 3 {
		t.Fatalf("json status err=%v", err)
	}
}

func TestEnsureMakesSeedingIdempotent(t *testing.T) {
	ts, b := testServer(t)
	env := envOf(map[string]string{"AGENTBOARD_URL": ts.URL})
	mustRun := func(args ...string) string {
		code, out, errw := run(t, env, args...)
		if code != 0 {
			t.Fatalf("%v: %d %s", args, code, errw)
		}
		return strings.TrimSpace(out)
	}
	mustRun("project", "add", "LOOP", "Loop")
	seed := func() (epic, story, sub string) {
		epic = mustRun("task", "add", "-p", "LOOP", "-ensure", "-type", "epic", "Go OSS Loop")
		story = mustRun("task", "add", "-p", "LOOP", "-ensure", "-type", "story", "-parent", epic, "build v0.1.0")
		sub = mustRun("task", "add", "-p", "LOOP", "-ensure", "-parent", story, "scaffold")
		return
	}
	e1, s1, t1 := seed()
	e2, s2, t2 := seed()
	e3, s3, t3 := seed()
	if e1 != e2 || e2 != e3 || s1 != s2 || s2 != s3 || t1 != t2 || t2 != t3 {
		t.Fatalf("seeding created duplicates: %v %v %v / %v %v %v", e1, s1, t1, e3, s3, t3)
	}
	if n := len(b.Tasks(agentboard.Filter{})); n != 3 {
		t.Fatalf("board has %d tasks after 3 seed runs, want 3", n)
	}
	// Same title under a different parent is a different task.
	other := mustRun("task", "add", "-p", "LOOP", "-ensure", "-type", "story", "-parent", e1, "other story")
	if again := mustRun("task", "add", "-p", "LOOP", "-ensure", "-parent", other, "scaffold"); again == t1 {
		t.Fatal("-ensure conflated tasks with different parents")
	}
	// Without -ensure the same title is created again.
	if dup := mustRun("task", "add", "-p", "LOOP", "-parent", s1, "scaffold"); dup == t1 {
		t.Fatal("plain add must not dedupe")
	}
}

func TestCommandErrors(t *testing.T) {
	ts, _ := testServer(t)
	env := envOf(map[string]string{"AGENTBOARD_URL": ts.URL})
	run(t, env, "project", "add", "AB", "n")
	run(t, env, "task", "add", "-p", "AB", "one")
	tests := []struct {
		name string
		args []string
		code int
		err  string
	}{
		{"claim needs an agent", []string{"task", "claim", "AB-1"}, 2, "agent name"},
		{"done needs an agent", []string{"task", "done", "AB-1"}, 2, "agent name"},
		{"heartbeat needs an agent", []string{"agent", "heartbeat"}, 2, "agent name"},
		{"add needs a project", []string{"task", "add", "title"}, 2, "task add -p KEY"},
		{"add needs a title", []string{"task", "add", "-p", "AB"}, 2, "task add -p KEY"},
		{"show needs an id", []string{"task", "show"}, 2, "task show ID"},
		{"comment needs text", []string{"task", "comment", "AB-1"}, 2, "task comment ID TEXT"},
		{"bad meta", []string{"agent", "heartbeat", "-agent", "a", "-meta", "novalue"}, 2, "key=value"},
		{"unknown task", []string{"task", "show", "AB-99"}, 1, "404"},
		{"claim unknown task", []string{"task", "claim", "-agent", "a", "AB-99"}, 1, "404"},
		{"bad status", []string{"task", "update", "AB-1", "-status", "weird"}, 1, "400"},
		{"unknown project", []string{"task", "add", "-p", "NOPE", "x"}, 1, "404"},
		{"unreachable server", []string{"status", "-url", "http://127.0.0.1:1"}, 1, "agentboard:"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, _, errw := run(t, env, tc.args...)
			if code != tc.code || !strings.Contains(errw, tc.err) {
				t.Fatalf("code=%d err=%q", code, errw)
			}
		})
	}
	t.Run("claim conflict is an error", func(t *testing.T) {
		run(t, env, "task", "claim", "-agent", "a", "AB-1")
		code, _, errw := run(t, env, "task", "claim", "-agent", "b", "AB-1")
		if code != 1 || !strings.Contains(errw, "409") {
			t.Fatalf("code=%d err=%q", code, errw)
		}
	})
}

func TestUpdateOnlySendsGivenFlags(t *testing.T) {
	ts, b := testServer(t)
	env := envOf(map[string]string{"AGENTBOARD_URL": ts.URL})
	run(t, env, "project", "add", "AB", "n")
	run(t, env, "task", "add", "-p", "AB", "-priority", "high", "-labels", "keep", "-desc", "orig", "title")
	if code, _, errw := run(t, env, "task", "update", "AB-1", "-title", "renamed"); code != 0 {
		t.Fatal(errw)
	}
	d, _ := b.Task("AB-1")
	if d.Task.Title != "renamed" || d.Task.Priority != agentboard.PriorityHigh || d.Task.Description != "orig" || len(d.Task.Labels) != 1 {
		t.Fatalf("update clobbered untouched fields: %+v", d.Task)
	}
	// An explicit empty value clears; detaching a parent works too.
	run(t, env, "task", "update", "AB-1", "-labels", "", "-desc", "")
	d, _ = b.Task("AB-1")
	if len(d.Task.Labels) != 0 || d.Task.Description != "" {
		t.Fatalf("explicit empty values must clear: %+v", d.Task)
	}
}

func TestTokenIsSentAndStopNeedsShutdownEnabled(t *testing.T) {
	b, _ := agentboard.Open(agentboard.Options{SaveMode: agentboard.SaveSync})
	defer b.Close(context.Background())
	ts := httptest.NewServer(agentboard.NewServer(agentboard.ServerOptions{Board: b, Token: "s3"}).Handler())
	defer ts.Close()

	if code, _, errw := run(t, envOf(map[string]string{"AGENTBOARD_URL": ts.URL}), "status"); code != 1 || !strings.Contains(errw, "401") {
		t.Fatalf("without token: %d %q", code, errw)
	}
	env := envOf(map[string]string{"AGENTBOARD_URL": ts.URL, "AGENTBOARD_TOKEN": "s3"})
	if code, _, errw := run(t, env, "status"); code != 0 {
		t.Fatalf("with env token: %d %q", code, errw)
	}
	if code, _, errw := run(t, noEnv, "status", "-url", ts.URL, "-token", "s3"); code != 0 {
		t.Fatalf("with flag token: %d %q", code, errw)
	}
	if code, _, errw := run(t, env, "stop"); code != 1 || !strings.Contains(errw, "disabled") {
		t.Fatalf("stop on a server without shutdown: %d %q", code, errw)
	}
}

// --- serve ---------------------------------------------------------------------

func TestServeRefusals(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name string
		args []string
		code int
		err  string
	}{
		{"public bind needs a token", []string{"serve", "-addr", "0.0.0.0:0", "-data", dir}, 2, "without a token"},
		{"bad save mode", []string{"serve", "-addr", "127.0.0.1:0", "-data", dir, "-save-mode", "maybe"}, 2, "async or sync"},
		{"bad webhook", []string{"serve", "-addr", "127.0.0.1:0", "-data", dir, "-webhook", "ftp://x"}, 2, "webhook"},
		{"address in use", nil, 1, "address"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := tc.args
			if tc.name == "address in use" {
				ts := httptest.NewServer(http.NotFoundHandler())
				defer ts.Close()
				args = []string{"serve", "-addr", strings.TrimPrefix(ts.URL, "http://"), "-data", dir}
			}
			code, _, errw := run(t, noEnv, args...)
			if code != tc.code || !strings.Contains(errw, tc.err) {
				t.Fatalf("code=%d err=%q", code, errw)
			}
		})
	}
}

// syncBuffer lets the test read what the server printed while it runs.
type syncBuffer struct {
	mu  chan struct{}
	buf bytes.Buffer
}

func newSyncBuffer() *syncBuffer { return &syncBuffer{mu: make(chan struct{}, 1)} }

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu <- struct{}{}
	defer func() { <-s.mu }()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu <- struct{}{}
	defer func() { <-s.mu }()
	return s.buf.String()
}

var listenRE = regexp.MustCompile(`listening on (http://\S+)`)

// startServe runs "agentboard serve" in-process and returns its base URL.
func startServe(t *testing.T, dir string, extra ...string) (base string, stop func() int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	out := newSyncBuffer()
	code := make(chan int, 1)
	args := append([]string{"serve", "-addr", "127.0.0.1:0", "-data", dir, "-save-debounce", "10ms"}, extra...)
	go func() { code <- cli.Run(ctx, args, noEnv, out, out) }()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if m := listenRE.FindStringSubmatch(out.String()); m != nil {
			return m[1], func() int { cancel(); return <-code }
		}
		select {
		case c := <-code:
			t.Fatalf("serve exited early with %d:\n%s", c, out.String())
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel()
	t.Fatalf("serve never announced its address:\n%s", out.String())
	return "", nil
}

func TestServeStartsPersistsAndStops(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	base, stop := startServe(t, dir)
	env := envOf(map[string]string{"AGENTBOARD_URL": base})

	if resp, err := http.Get(base + "/healthz"); err != nil || resp.StatusCode != 200 {
		t.Fatalf("healthz: %v", err)
	}
	if resp, err := http.Get(base + "/app.wasm"); err != nil || resp.StatusCode != 200 {
		t.Fatalf("the UI is not served: %v", err)
	}
	if code, _, errw := run(t, env, "project", "add", "AB", "n"); code != 0 {
		t.Fatal(errw)
	}
	if code, _, errw := run(t, env, "task", "add", "-p", "AB", "persist me"); code != 0 {
		t.Fatal(errw)
	}
	if code := stop(); code != 0 {
		t.Fatalf("serve exit code %d after a graceful stop", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "board.json")); err != nil {
		t.Fatalf("no data file after stop: %v", err)
	}

	// A second run on the same directory sees the data (graceful stop
	// flushed the write-behind queue).
	base2, stop2 := startServe(t, dir)
	defer stop2()
	code, out, errw := run(t, envOf(map[string]string{"AGENTBOARD_URL": base2}), "task", "list")
	if code != 0 || !strings.Contains(out, "persist me") {
		t.Fatalf("data lost across restart: %d %q %q", code, out, errw)
	}
}

func TestStopCommandShutsTheServerDown(t *testing.T) {
	base, stop := startServe(t, t.TempDir())
	env := envOf(map[string]string{"AGENTBOARD_URL": base})
	code, out, errw := run(t, env, "stop")
	if code != 0 || !strings.Contains(out, "shutting down") {
		t.Fatalf("stop: %d %q %q", code, out, errw)
	}
	if code := stop(); code != 0 { // returns once Serve has exited
		t.Fatalf("serve exit code %d", code)
	}
	if _, err := http.Get(base + "/healthz"); err == nil {
		t.Fatal("server still answering after stop")
	}
}

func TestServeDisableShutdown(t *testing.T) {
	base, stop := startServe(t, t.TempDir(), "-disable-shutdown")
	defer stop()
	code, _, errw := run(t, envOf(map[string]string{"AGENTBOARD_URL": base}), "stop")
	if code != 1 || !strings.Contains(errw, "disabled") {
		t.Fatalf("stop with shutdown disabled: %d %q", code, errw)
	}
	if resp, err := http.Get(base + "/healthz"); err != nil || resp.StatusCode != 200 {
		t.Fatal("server must keep running")
	}
}

func TestServeSaveModeSyncWritesImmediately(t *testing.T) {
	dir := t.TempDir()
	base, stop := startServe(t, dir, "-save-mode", "sync")
	defer stop()
	env := envOf(map[string]string{"AGENTBOARD_URL": base})
	run(t, env, "project", "add", "AB", "n")
	// No wait, no flush: sync mode has already written the file.
	data, err := os.ReadFile(filepath.Join(dir, "board.json"))
	if err != nil || !strings.Contains(string(data), `"AB"`) {
		t.Fatalf("sync mode did not write inline: %v %q", err, data)
	}
}
