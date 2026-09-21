// Package cli implements the agentboard command line. It is separate from
// cmd/agentboard so that tests can drive it in-process with injected
// environment and output.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/JiaBao-do/agentboard"
)

// Version is set at build time (-ldflags "-X .../internal/cli.Version=v0.1.0");
// otherwise the module version recorded by "go install" is used.
var Version = "dev"

const (
	defaultURL  = "http://127.0.0.1:7878"
	defaultAddr = "127.0.0.1:7878"
	defaultData = ".agentboard"
	boardFile   = "board.json"
)

const usage = `agentboard: a small task board that shows which agent is working on what.

Usage:
  agentboard serve [flags]                    run the server and web UI
  agentboard stop                             stop a running server (needs shutdown enabled)
  agentboard status                           board summary and agent presence
  agentboard project add KEY NAME             create a project (idempotent)
  agentboard task add -p KEY [flags] TITLE    create a task (-ensure: only if the title is new)
  agentboard task list [flags]                list tasks
  agentboard task show ID                     show a task and its timeline
  agentboard task claim ID [-lease 10m]       claim a task for -agent
  agentboard task update ID [flags]           change status, priority, title, ...
  agentboard task done ID                     mark a task done
  agentboard task release ID                  give a claimed task back
  agentboard task comment ID TEXT             add a comment
  agentboard agent heartbeat [flags]          report that -agent is alive
  agentboard export [-o FILE] [-gzip]         write the whole board as readable JSON (-data DIR: offline)
  agentboard dump                             print the board as JSON to stdout
  agentboard import FILE -data DIR            replace the board from an export (offline; server must be stopped)
  agentboard version

Client commands talk to a running server. Common flags (env in brackets):
  -url    server address        [AGENTBOARD_URL]   (default ` + defaultURL + `)
  -token  access token          [AGENTBOARD_TOKEN]
  -agent  your agent name       [AGENTBOARD_AGENT]
  -json   machine readable output

Run "agentboard serve -h" or "agentboard task add -h" for flags of one command.
`

// Run executes the command line args (without the program name) and returns
// the process exit code. env looks up environment variables.
func Run(ctx context.Context, args []string, env func(string) string, stdout, stderr io.Writer) int {
	a := &app{ctx: ctx, env: env, out: stdout, errw: stderr}
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	var err error
	switch args[0] {
	case "serve":
		err = a.serve(args[1:])
	case "stop":
		err = a.stop(args[1:])
	case "status":
		err = a.status(args[1:])
	case "project":
		err = a.project(args[1:])
	case "task":
		err = a.task(args[1:])
	case "agent":
		err = a.agent(args[1:])
	case "export":
		err = a.export(args[1:], false)
	case "dump":
		err = a.export(args[1:], true)
	case "import":
		err = a.importCmd(args[1:])
	case "version", "-v", "--version":
		fmt.Fprintln(stdout, "agentboard", version())
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
	default:
		fmt.Fprintf(stderr, "agentboard: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
	var ue usageError
	switch {
	case err == nil:
		return 0
	case errors.Is(err, flag.ErrHelp):
		return 0
	case errors.As(err, &ue):
		fmt.Fprintf(stderr, "agentboard: %v\n", err)
		return 2
	default:
		fmt.Fprintf(stderr, "agentboard: %v\n", err)
		return 1
	}
}

func version() string {
	if Version != "dev" {
		return Version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return Version
}

// usageError marks mistakes in how the command was invoked (exit code 2).
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error { return usageError{fmt.Sprintf(format, a...)} }

type app struct {
	ctx  context.Context
	env  func(string) string
	out  io.Writer
	errw io.Writer
}

func (a *app) getenv(key, def string) string {
	if v := a.env(key); v != "" {
		return v
	}
	return def
}

// newFlags builds a FlagSet that reports errors as values and prints its help
// to stderr.
func (a *app) newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(a.errw)
	return fs
}

// parse parses flags that may appear before, between or after positional
// arguments, and returns the positional ones.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, err
			}
			return nil, usageError{err.Error()}
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// clientFlags are shared by every command that talks to a server.
type clientFlags struct {
	url, token, agent string
	json              bool
}

func (a *app) addClientFlags(fs *flag.FlagSet) *clientFlags {
	c := &clientFlags{}
	fs.StringVar(&c.url, "url", a.getenv("AGENTBOARD_URL", defaultURL), "server `URL`")
	fs.StringVar(&c.token, "token", a.env("AGENTBOARD_TOKEN"), "access `token`")
	fs.StringVar(&c.agent, "agent", a.env("AGENTBOARD_AGENT"), "your agent `name`")
	fs.BoolVar(&c.json, "json", false, "machine readable JSON output")
	return c
}

func (c *clientFlags) client() *agentboard.Client {
	cl := agentboard.NewClient(c.url, c.token)
	cl.Agent = c.agent
	return cl
}

func (c *clientFlags) needAgent() (string, error) {
	if c.agent == "" {
		return "", usagef("this command needs an agent name: use -agent or AGENTBOARD_AGENT")
	}
	return c.agent, nil
}

func (a *app) printJSON(v any) error {
	enc := json.NewEncoder(a.out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// ---- serve -----------------------------------------------------------------

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func (a *app) serve(args []string) error {
	fs := a.newFlags("serve")
	var (
		addr        = fs.String("addr", a.getenv("AGENTBOARD_ADDR", defaultAddr), "listen `address`; non-loopback needs a token [AGENTBOARD_ADDR]")
		data        = fs.String("data", a.getenv("AGENTBOARD_DATA", defaultData), "data `directory` [AGENTBOARD_DATA]")
		token       = fs.String("token", a.env("AGENTBOARD_TOKEN"), "access `token` required on /api/ [AGENTBOARD_TOKEN]")
		lease       = fs.Duration("lease", 10*time.Minute, "default task lease")
		agentTTL    = fs.Duration("agent-ttl", 2*time.Minute, "an agent is online this long after its last heartbeat")
		saveMode    = fs.String("save-mode", a.getenv("AGENTBOARD_SAVE_MODE", "async"), "async (write-behind) or sync (save on every change)")
		debounce    = fs.Duration("save-debounce", 200*time.Millisecond, "async: wait this long for more changes before saving")
		maxActivity = fs.Int("max-activity", 5000, "archive the oldest activity entries once the live log exceeds this many")
		forceUnlock = fs.Bool("force-unlock", false, "remove a stale lock file, only if its recorded process is provably dead")
		maxLatency  = fs.Duration("save-max-latency", 2*time.Second, "async: never postpone a save longer than this")
		noShutdown  = fs.Bool("disable-shutdown", a.env("AGENTBOARD_DISABLE_SHUTDOWN") != "", "turn off POST /api/admin/shutdown [AGENTBOARD_DISABLE_SHUTDOWN]")
		webhookURL  = fs.String("webhook", a.env("AGENTBOARD_WEBHOOK"), "POST every change event to this `URL` [AGENTBOARD_WEBHOOK]")
		webhookKey  = fs.String("webhook-secret", a.env("AGENTBOARD_WEBHOOK_SECRET"), "sign webhook bodies with this `secret` [AGENTBOARD_WEBHOOK_SECRET]")
		allowedHost stringList
	)
	fs.Var(&allowedHost, "allow-host", "extra Host `name` to accept (repeatable), e.g. behind a reverse proxy")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if *webhookURL != "" {
		if err := agentboard.ValidateWebhookURL(*webhookURL); err != nil {
			return usagef("%v", err)
		}
	}
	mode := agentboard.SaveMode(*saveMode)
	if mode != agentboard.SaveAsync && mode != agentboard.SaveSync {
		return usagef("-save-mode must be async or sync, got %q", *saveMode)
	}

	ln, err := (&net.ListenConfig{}).Listen(a.ctx, "tcp", *addr)
	if err != nil {
		return err
	}
	defer ln.Close()
	loopback := false
	if ta, ok := ln.Addr().(*net.TCPAddr); ok {
		loopback = ta.IP.IsLoopback()
	}
	if !loopback && *token == "" {
		return usagef("refusing to listen on %s without a token: set -token or AGENTBOARD_TOKEN, or bind to 127.0.0.1", ln.Addr())
	}

	if err := os.MkdirAll(*data, 0o750); err != nil {
		return err
	}
	if *forceUnlock {
		if err := agentboard.ForceUnlock(*data); err != nil {
			return err
		}
	}
	lock, err := agentboard.AcquireLock(*data)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }() // runs last: after the final save
	if err := lock.SetAddr(ln.Addr().String()); err != nil {
		return err
	}
	board, err := agentboard.Open(agentboard.Options{
		Store:      agentboard.NewFileStore(filepath.Join(*data, boardFile)),
		ArchiveDir: filepath.Join(*data, "archive"), MaxActivity: *maxActivity,
		Lease:    *lease,
		AgentTTL: *agentTTL,
		SaveMode: mode, SaveDebounce: *debounce, SaveMaxLatency: *maxLatency,
	})
	if err != nil {
		return err
	}
	defer func() {
		if cerr := board.Close(context.Background()); cerr != nil {
			fmt.Fprintf(a.errw, "agentboard: final save failed: %v\n", cerr)
		}
	}()

	so := agentboard.ServerOptions{
		Board:          board,
		Token:          *token,
		EnableShutdown: !*noShutdown,
	}
	if loopback {
		so.AllowedHosts = []string{"localhost", "127.0.0.1", "::1"}
	}
	so.AllowedHosts = append(so.AllowedHosts, allowedHost...)
	if *webhookURL != "" {
		wctx, cancel := context.WithCancel(a.ctx)
		wait := (&agentboard.Webhook{URL: *webhookURL, Secret: *webhookKey}).Start(wctx, board)
		defer func() { cancel(); wait() }()
	}

	fmt.Fprintf(a.out, "agentboard listening on http://%s (data: %s, save: %s)\n", ln.Addr(), *data, mode)
	if so.EnableShutdown {
		fmt.Fprintln(a.out, "stop it with Ctrl-C, `agentboard stop`, or the Stop button in the UI")
	}
	return agentboard.NewServer(so).Serve(a.ctx, ln)
}

func (a *app) stop(args []string) error {
	fs := a.newFlags("stop")
	c := a.addClientFlags(fs)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if err := c.client().Shutdown(a.ctx); err != nil {
		return err
	}
	fmt.Fprintln(a.out, "server is shutting down")
	return nil
}

// ---- status ------------------------------------------------------------------

func (a *app) status(args []string) error {
	fs := a.newFlags("status")
	c := a.addClientFlags(fs)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	snap, err := c.client().State(a.ctx)
	if err != nil {
		return err
	}
	if c.json {
		return a.printJSON(snap)
	}
	counts := map[agentboard.Status]int{}
	for _, t := range snap.Tasks {
		counts[t.Status]++
	}
	fmt.Fprintf(a.out, "%s  (save: %s, %d saves, %d failures)\n", c.url, snap.Save.State(), snap.Save.Saves, snap.Save.Failures)
	var parts []string
	for _, s := range []agentboard.Status{agentboard.StatusTodo, agentboard.StatusInProgress, agentboard.StatusReview, agentboard.StatusDone, agentboard.StatusBlocked} {
		parts = append(parts, fmt.Sprintf("%s %d", s, counts[s]))
	}
	fmt.Fprintln(a.out, strings.Join(parts, " | "))
	if len(snap.Agents) == 0 {
		fmt.Fprintln(a.out, "no agents have reported in")
		return nil
	}
	titles := map[string]string{}
	for _, t := range snap.Tasks {
		titles[t.ID] = t.Title
	}
	fmt.Fprintln(a.out, "agents:")
	for _, ag := range snap.Agents {
		mark := "o"
		if ag.Online {
			mark = "*"
		}
		work := "idle"
		if ag.CurrentTask != "" {
			work = fmt.Sprintf("working on %s %q", ag.CurrentTask, titles[ag.CurrentTask])
		}
		fmt.Fprintf(a.out, "  %s %-20s %-12s %s\n", mark, ag.Name, ag.Kind, work)
	}
	return nil
}

// ---- project -----------------------------------------------------------------

func (a *app) project(args []string) error {
	if len(args) == 0 || args[0] != "add" {
		return usagef("usage: agentboard project add KEY NAME")
	}
	fs := a.newFlags("project add")
	c := a.addClientFlags(fs)
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) < 2 {
		return usagef("usage: agentboard project add KEY NAME")
	}
	if _, err := c.needAgent(); err != nil {
		return err
	}
	p, err := c.client().CreateProject(a.ctx, agentboard.ProjectRequest{Key: pos[0], Name: strings.Join(pos[1:], " "), Actor: c.agent})
	var ae *agentboard.APIError
	if errors.As(err, &ae) && ae.Status == 409 {
		fmt.Fprintf(a.out, "%s (exists)\n", pos[0])
		return nil
	}
	if err != nil {
		return err
	}
	if c.json {
		return a.printJSON(p)
	}
	fmt.Fprintln(a.out, p.Key)
	return nil
}

// ---- task ----------------------------------------------------------------------

func (a *app) task(args []string) error {
	if len(args) == 0 {
		return usagef("usage: agentboard task add|list|show|claim|update|done|release|comment ...")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "add":
		return a.taskAdd(rest)
	case "list":
		return a.taskList(rest)
	case "show":
		return a.taskShow(rest)
	case "claim":
		return a.taskClaim(rest)
	case "update":
		return a.taskUpdate(rest)
	case "done":
		return a.taskAgentOp(rest, "done")
	case "release":
		return a.taskAgentOp(rest, "release")
	case "comment":
		return a.taskComment(rest)
	}
	return usagef("unknown task command %q", sub)
}

func (a *app) taskAdd(args []string) error {
	fs := a.newFlags("task add")
	c := a.addClientFlags(fs)
	var (
		project  = fs.String("p", a.env("AGENTBOARD_PROJECT"), "project `key` [AGENTBOARD_PROJECT]")
		kind     = fs.String("type", "task", "epic, story or task")
		parent   = fs.String("parent", "", "parent task `ID` (a story's epic, a task's story)")
		priority = fs.String("priority", "medium", "low, medium, high or urgent")
		labels   = fs.String("labels", "", "comma separated labels")
		desc     = fs.String("desc", "", "description")
		ensure   = fs.Bool("ensure", false, "do nothing if a task with this title and parent already exists (idempotent)")
	)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if *project == "" || len(pos) == 0 {
		return usagef("usage: agentboard task add -p KEY [flags] TITLE")
	}
	if _, err := c.needAgent(); err != nil {
		return err
	}
	title := strings.Join(pos, " ")
	cl := c.client()
	if *ensure {
		existing, err := cl.Tasks(a.ctx, agentboard.Filter{Project: *project, Parent: *parent})
		if err != nil {
			return err
		}
		for _, t := range existing {
			if t.Title == title && string(t.Type) == *kind {
				fmt.Fprintf(a.errw, "%s already exists\n", t.ID)
				return a.printTaskID(c, t)
			}
		}
	}
	t, err := cl.AddTask(a.ctx, agentboard.NewTask{
		Project: *project, Type: agentboard.Kind(*kind), Parent: *parent, Title: title,
		Description: *desc, Priority: agentboard.Priority(*priority), Labels: splitList(*labels), Actor: c.agent,
	})
	if err != nil {
		return err
	}
	return a.printTaskID(c, t)
}

func (a *app) printTaskID(c *clientFlags, t agentboard.Task) error {
	if c.json {
		return a.printJSON(t)
	}
	fmt.Fprintln(a.out, t.ID)
	return nil
}

func splitList(s string) []string {
	out := []string{} // never nil: "labels": [] clears, "labels": null would be ignored
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (a *app) taskList(args []string) error {
	fs := a.newFlags("task list")
	c := a.addClientFlags(fs)
	var (
		project  = fs.String("p", "", "only this project `key`")
		status   = fs.String("status", "", "only this status")
		assignee = fs.String("assignee", "", "only tasks of this agent")
		kind     = fs.String("type", "", "only epics, stories or tasks")
		parent   = fs.String("parent", "", "only children of this task")
	)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	tasks, err := c.client().Tasks(a.ctx, agentboard.Filter{
		Project: *project, Status: agentboard.Status(*status), Assignee: *assignee,
		Type: agentboard.Kind(*kind), Parent: *parent,
	})
	if err != nil {
		return err
	}
	if c.json {
		return a.printJSON(tasks)
	}
	tw := tabwriter.NewWriter(a.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTYPE\tSTATUS\tPRIO\tAGENT\tTITLE")
	for _, t := range tasks {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", t.ID, t.Type, t.Status, t.Priority, dash(t.Assignee), t.Title)
	}
	return tw.Flush()
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (a *app) taskShow(args []string) error {
	fs := a.newFlags("task show")
	c := a.addClientFlags(fs)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: agentboard task show ID")
	}
	d, err := c.client().Task(a.ctx, pos[0])
	if err != nil {
		return err
	}
	if c.json {
		return a.printJSON(d)
	}
	t := d.Task
	fmt.Fprintf(a.out, "%s  %s\n", t.ID, t.Title)
	fmt.Fprintf(a.out, "  type %s | status %s | priority %s | agent %s | parent %s\n", t.Type, t.Status, t.Priority, dash(t.Assignee), dash(t.Parent))
	if t.LeaseExpires != nil {
		fmt.Fprintf(a.out, "  lease until %s\n", t.LeaseExpires.Local().Format(time.RFC3339))
	}
	if t.Description != "" {
		fmt.Fprintf(a.out, "\n%s\n", t.Description)
	}
	fmt.Fprintln(a.out, "\ntimeline:")
	for _, e := range d.Activity {
		fmt.Fprintf(a.out, "  %s  %-14s %s %s\n", e.Time.Local().Format("2006-01-02 15:04:05"), e.Actor, e.Action, e.Detail)
	}
	return nil
}

func (a *app) taskClaim(args []string) error {
	fs := a.newFlags("task claim")
	c := a.addClientFlags(fs)
	lease := fs.Duration("lease", 0, "lease length (default: the server's)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	agent, err := c.needAgent()
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: agentboard task claim ID [-lease 10m]")
	}
	t, err := c.client().Claim(a.ctx, pos[0], agent, *lease)
	if err != nil {
		return err
	}
	return a.printTaskID(c, t)
}

func (a *app) taskAgentOp(args []string, op string) error {
	fs := a.newFlags("task " + op)
	c := a.addClientFlags(fs)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	agent, err := c.needAgent()
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: agentboard task %s ID", op)
	}
	var t agentboard.Task
	if op == "done" {
		t, err = c.client().Done(a.ctx, pos[0], agent)
	} else {
		t, err = c.client().Release(a.ctx, pos[0], agent)
	}
	if err != nil {
		return err
	}
	return a.printTaskID(c, t)
}

func (a *app) taskUpdate(args []string) error {
	fs := a.newFlags("task update")
	c := a.addClientFlags(fs)
	var (
		status   = fs.String("status", "", "todo, in_progress, review, done or blocked")
		priority = fs.String("priority", "", "low, medium, high or urgent")
		title    = fs.String("title", "", "new title")
		desc     = fs.String("desc", "", "new description")
		labels   = fs.String("labels", "", "replace labels (comma separated)")
		parent   = fs.String("parent", "", "new parent ID")
	)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: agentboard task update ID [flags]")
	}
	if _, err := c.needAgent(); err != nil {
		return err
	}
	p := agentboard.Patch{Actor: c.agent}
	fs.Visit(func(f *flag.Flag) { // only flags that were actually given
		switch f.Name {
		case "status":
			s := agentboard.Status(*status)
			p.Status = &s
		case "priority":
			pr := agentboard.Priority(*priority)
			p.Priority = &pr
		case "title":
			p.Title = title
		case "desc":
			p.Description = desc
		case "labels":
			l := splitList(*labels)
			p.Labels = &l
		case "parent":
			p.Parent = parent
		}
	})
	t, err := c.client().Update(a.ctx, pos[0], p)
	if err != nil {
		return err
	}
	return a.printTaskID(c, t)
}

func (a *app) taskComment(args []string) error {
	fs := a.newFlags("task comment")
	c := a.addClientFlags(fs)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 2 {
		return usagef("usage: agentboard task comment ID TEXT")
	}
	if _, err := c.needAgent(); err != nil {
		return err
	}
	if err := c.client().Comment(a.ctx, pos[0], c.agent, strings.Join(pos[1:], " ")); err != nil {
		return err
	}
	fmt.Fprintln(a.out, "ok")
	return nil
}

// ---- agent ----------------------------------------------------------------------

func (a *app) agent(args []string) error {
	if len(args) == 0 || args[0] != "heartbeat" {
		return usagef("usage: agentboard agent heartbeat [-agent NAME] [-kind KIND] [-task ID] [-meta k=v]")
	}
	fs := a.newFlags("agent heartbeat")
	c := a.addClientFlags(fs)
	var (
		kind = fs.String("kind", "", "free-text kind of agent (default: agent)")
		task = fs.String("task", "", "the task you are working on")
		meta stringList
	)
	fs.Var(&meta, "meta", "`key=value` describing the agent (repeatable)")
	if _, err := parse(fs, args[1:]); err != nil {
		return err
	}
	agent, err := c.needAgent()
	if err != nil {
		return err
	}
	req := agentboard.HeartbeatRequest{Kind: *kind, Task: *task}
	if len(meta) > 0 {
		req.Meta = map[string]string{}
		for _, kv := range meta {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || k == "" {
				return usagef("-meta wants key=value, got %q", kv)
			}
			req.Meta[k] = v
		}
	}
	ag, err := c.client().Heartbeat(a.ctx, agent, req)
	if err != nil {
		return err
	}
	if c.json {
		return a.printJSON(ag)
	}
	fmt.Fprintf(a.out, "%s online\n", ag.Name)
	return nil
}
