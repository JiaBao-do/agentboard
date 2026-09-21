package agentboard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// APIError is returned by Client for any non-2xx response.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("agentboard: %d %s", e.Status, e.Message) }

// Client talks to a running agentboard server. Agents, hooks and the CLI use
// it. The zero HTTP field uses a client with a 30 second timeout.
type Client struct {
	BaseURL string
	Token   string
	// Agent is sent as X-Agent-Name so writes are attributed to it when the
	// request itself does not name an actor.
	Agent string
	HTTP  *http.Client
}

// NewClient returns a Client for the server at baseURL, such as
// "http://127.0.0.1:7878". token may be empty.
func NewClient(baseURL, token string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: token}
}

func (c *Client) do(ctx context.Context, method, p string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+p, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if c.Agent != "" {
		req.Header.Set(actorHeader, c.Agent)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var ae apiError
		msg := strings.TrimSpace(string(data))
		if json.Unmarshal(data, &ae) == nil && ae.Error != "" {
			msg = ae.Error
		}
		return &APIError{Status: resp.StatusCode, Message: msg}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// State fetches the whole board.
func (c *Client) State(ctx context.Context) (Snapshot, error) {
	var s Snapshot
	return s, c.do(ctx, http.MethodGet, "/api/state", nil, &s)
}

// CreateProject creates a project.
func (c *Client) CreateProject(ctx context.Context, req ProjectRequest) (Project, error) {
	var p Project
	return p, c.do(ctx, http.MethodPost, "/api/projects", req, &p)
}

// AddTask creates a task.
func (c *Client) AddTask(ctx context.Context, in NewTask) (Task, error) {
	var t Task
	return t, c.do(ctx, http.MethodPost, "/api/tasks", in, &t)
}

// Tasks lists tasks matching f.
func (c *Client) Tasks(ctx context.Context, f Filter) ([]Task, error) {
	q := url.Values{}
	if f.Project != "" {
		q.Set("project", f.Project)
	}
	if f.Status != "" {
		q.Set("status", string(f.Status))
	}
	if f.Assignee != "" {
		q.Set("assignee", f.Assignee)
	}
	if f.Type != "" {
		q.Set("type", string(f.Type))
	}
	if f.Parent != "" {
		q.Set("parent", f.Parent)
	}
	p := "/api/tasks"
	if len(q) > 0 {
		p += "?" + q.Encode()
	}
	var ts []Task
	return ts, c.do(ctx, http.MethodGet, p, nil, &ts)
}

// Task fetches one task with its timeline.
func (c *Client) Task(ctx context.Context, id string) (TaskDetail, error) {
	var d TaskDetail
	return d, c.do(ctx, http.MethodGet, "/api/tasks/"+url.PathEscape(id), nil, &d)
}

// Update applies a partial update to a task.
func (c *Client) Update(ctx context.Context, id string, p Patch) (Task, error) {
	var t Task
	return t, c.do(ctx, http.MethodPatch, "/api/tasks/"+url.PathEscape(id), p, &t)
}

// Claim claims a task for agent with the given lease (zero: server default).
func (c *Client) Claim(ctx context.Context, id, agent string, lease time.Duration) (Task, error) {
	var t Task
	req := ClaimRequest{Agent: agent, LeaseSeconds: int(lease / time.Second)}
	return t, c.do(ctx, http.MethodPost, "/api/tasks/"+url.PathEscape(id)+"/claim", req, &t)
}

// Release gives a claimed task back.
func (c *Client) Release(ctx context.Context, id, agent string) (Task, error) {
	var t Task
	return t, c.do(ctx, http.MethodPost, "/api/tasks/"+url.PathEscape(id)+"/release", AgentRequest{Agent: agent}, &t)
}

// Done marks a task done.
func (c *Client) Done(ctx context.Context, id, agent string) (Task, error) {
	var t Task
	return t, c.do(ctx, http.MethodPost, "/api/tasks/"+url.PathEscape(id)+"/done", AgentRequest{Agent: agent}, &t)
}

// Comment adds a comment to a task's timeline.
func (c *Client) Comment(ctx context.Context, id, actor, text string) error {
	return c.do(ctx, http.MethodPost, "/api/tasks/"+url.PathEscape(id)+"/comment", CommentRequest{Actor: actor, Text: text}, nil)
}

// Heartbeat reports that an agent is alive and extends its leases.
func (c *Client) Heartbeat(ctx context.Context, agent string, req HeartbeatRequest) (Agent, error) {
	var a Agent
	return a, c.do(ctx, http.MethodPost, "/api/agents/"+url.PathEscape(agent)+"/heartbeat", req, &a)
}

// Export downloads the whole board state, for backups and moving a board
// between machines. Write it with EncodeState.
func (c *Client) Export(ctx context.Context) (*State, error) {
	st := &State{}
	if err := c.do(ctx, http.MethodGet, "/api/export", nil, st); err != nil {
		return nil, err
	}
	return st, nil
}

// Shutdown asks the server to stop gracefully. The server must have been
// started with shutdown enabled. It returns once the server has accepted the
// request; the server then finishes in-flight requests and saves.
func (c *Client) Shutdown(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/admin/shutdown", bytes.NewReader([]byte("{}")))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agentboard-Action", "shutdown")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusAccepted {
		var ae apiError
		msg := strings.TrimSpace(string(data))
		if json.Unmarshal(data, &ae) == nil && ae.Error != "" {
			msg = ae.Error
		}
		return &APIError{Status: resp.StatusCode, Message: msg}
	}
	return nil
}
