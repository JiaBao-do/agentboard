package cli

import (
	"errors"
	"fmt"

	"github.com/JiaBao-do/agentboard"
)

// demo seeds a realistic board so a fresh install is not empty. It is
// idempotent: running it again adds nothing.
func (a *app) demo(args []string) error {
	fs := a.newFlags("demo")
	c := a.addClientFlags(fs)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cl := c.client()
	ctx := a.ctx

	if _, err := cl.CreateProject(ctx, agentboard.ProjectRequest{Key: "DEMO", Name: "Demo: ship a small library", Actor: "user"}); err != nil {
		var ae *agentboard.APIError
		if !errors.As(err, &ae) || ae.Status != 409 {
			return err
		}
	}
	existing, err := cl.Tasks(ctx, agentboard.Filter{Project: "DEMO"})
	if err != nil {
		return err
	}
	find := func(title string) (agentboard.Task, bool) {
		for _, t := range existing {
			if t.Title == title {
				return t, true
			}
		}
		return agentboard.Task{}, false
	}
	add := func(kind agentboard.Kind, parent, title string, prio agentboard.Priority, actor string) (agentboard.Task, bool, error) {
		if t, ok := find(title); ok {
			return t, false, nil
		}
		t, err := cl.AddTask(ctx, agentboard.NewTask{Project: "DEMO", Type: kind, Parent: parent, Title: title, Priority: prio, Actor: actor, Labels: []string{"demo"}})
		return t, err == nil, err
	}

	epic, _, err := add(agentboard.KindEpic, "", "Ship v1.0", agentboard.PriorityHigh, "user")
	if err != nil {
		return err
	}
	story, _, err := add(agentboard.KindStory, epic.ID, "Build the parser", agentboard.PriorityHigh, "planner-bot")
	if err != nil {
		return err
	}
	work := []struct {
		title, agent, status string
		prio                 agentboard.Priority
	}{
		{"Write the tokenizer", "coder-bot", "done", agentboard.PriorityMedium},
		{"Parse expressions", "coder-bot", "in_progress", agentboard.PriorityHigh},
		{"Fuzz the parser", "tester-bot", "review", agentboard.PriorityMedium},
		{"Publish the docs site", "", "blocked", agentboard.PriorityLow},
		{"Add benchmarks", "", "todo", agentboard.PriorityMedium},
		{"Fix the panic on empty input", "", "todo", agentboard.PriorityUrgent},
	}
	created := 0
	for _, w := range work {
		actor := w.agent
		if actor == "" {
			actor = "planner-bot"
		}
		t, isNew, err := add(agentboard.KindTask, story.ID, w.title, w.prio, actor)
		if err != nil {
			return err
		}
		if !isNew {
			continue
		}
		created++
		switch w.status {
		case "done":
			if _, err = cl.Claim(ctx, t.ID, w.agent, 0); err == nil {
				_, err = cl.Done(ctx, t.ID, w.agent)
			}
		case "in_progress":
			_, err = cl.Claim(ctx, t.ID, w.agent, 0)
		case "review":
			if _, err = cl.Claim(ctx, t.ID, w.agent, 0); err == nil {
				s := agentboard.StatusReview
				_, err = cl.Update(ctx, t.ID, agentboard.Patch{Status: &s, Actor: w.agent})
			}
		case "blocked":
			s := agentboard.StatusBlocked
			_, err = cl.Update(ctx, t.ID, agentboard.Patch{Status: &s, Actor: "planner-bot"})
			if err == nil {
				err = cl.Comment(ctx, t.ID, "planner-bot", "waiting for the domain name")
			}
		}
		if err != nil {
			return err
		}
	}
	for agent, kind := range map[string]string{"coder-bot": "coder", "tester-bot": "tester", "planner-bot": "planner"} {
		req := agentboard.HeartbeatRequest{Kind: kind, Meta: map[string]string{"demo": "true"}}
		if agent == "coder-bot" {
			if cur, ok := find("Parse expressions"); ok {
				req.Task = cur.ID
			} else if t, err := cl.Tasks(ctx, agentboard.Filter{Project: "DEMO", Assignee: agent, Status: agentboard.StatusInProgress}); err == nil && len(t) > 0 {
				req.Task = t[0].ID
			}
		}
		if _, err := cl.Heartbeat(ctx, agent, req); err != nil {
			return err
		}
	}
	fmt.Fprintf(a.out, "demo board ready: project DEMO, %d new tasks (open the UI and pick project DEMO)\n", created)
	return nil
}
