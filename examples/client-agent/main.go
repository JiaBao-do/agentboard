// client-agent: a Go program that behaves like an AI agent: it registers,
// claims a task with a lease, heartbeats, moves it to review, comments and
// marks it done, all through agentboard.Client.
//
//	go run ./examples/client-agent                      # starts its own throwaway board
//	go run ./examples/client-agent -url http://127.0.0.1:7878
//
// The output is deterministic (see expected_output.txt); the test in this
// directory checks it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http/httptest"
	"os"
	"time"

	"github.com/JiaBao-do/agentboard"
)

func main() {
	url := flag.String("url", "", "server URL (default: start an in-process throwaway board)")
	flag.Parse()
	ctx := context.Background()
	if *url == "" {
		board, err := agentboard.Open(agentboard.Options{})
		if err != nil {
			log.Fatal(err)
		}
		defer board.Close(ctx)
		ts := httptest.NewUnstartedServer(agentboard.NewServer(agentboard.ServerOptions{Board: board}).Handler())
		ts.Listener.Close()
		ts.Listener, _ = (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
		ts.Start()
		defer ts.Close()
		*url = ts.URL
	}
	c := agentboard.NewClient(*url, os.Getenv("AGENTBOARD_TOKEN"))
	c.Agent = "example-agent" // every write is attributed to this name
	if err := run(ctx, os.Stdout, c); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, w io.Writer, c *agentboard.Client) error {
	// Set up something to work on (idempotent enough for a demo).
	if _, err := c.CreateProject(ctx, agentboard.ProjectRequest{Key: "DEMO", Name: "Demo project"}); err != nil {
		var ae *agentboard.APIError
		if !isConflict(err, &ae) {
			return err
		}
	}
	task, err := c.AddTask(ctx, agentboard.NewTask{Project: "DEMO", Title: "Write the release notes", Priority: agentboard.PriorityHigh})
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "created %s (%s)\n", task.ID, task.Status)

	// 1. Register and stay visible. Send this every minute or so; the agent
	//    shows as offline two minutes after its last heartbeat.
	a, err := c.Heartbeat(ctx, "example-agent", agentboard.HeartbeatRequest{Kind: "go-program", Meta: map[string]string{"role": "writer"}})
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "agent %s online=%v\n", a.Name, a.Online)

	// 2. Claim with a lease. If this agent dies, the task returns to todo
	//    when the lease runs out; heartbeats extend it.
	claimed, err := c.Claim(ctx, task.ID, "example-agent", 5*time.Minute)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "claimed %s -> %s, assignee %s\n", claimed.ID, claimed.Status, claimed.Assignee)
	if _, err := c.Heartbeat(ctx, "example-agent", agentboard.HeartbeatRequest{Task: task.ID}); err != nil {
		return err
	}

	// 3. Report progress.
	review := agentboard.StatusReview
	if _, err := c.Update(ctx, task.ID, agentboard.Patch{Status: &review}); err != nil {
		return err
	}
	if err := c.Comment(ctx, task.ID, "example-agent", "draft is ready for a look"); err != nil {
		return err
	}

	// 4. Finish.
	done, err := c.Done(ctx, task.ID, "example-agent")
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "done %s -> %s\n", done.ID, done.Status)

	d, err := c.Task(ctx, task.ID)
	if err != nil {
		return err
	}
	for _, e := range d.Activity {
		fmt.Fprintf(w, "  %-8s %s\n", e.Action, e.Actor)
	}
	return nil
}

func isConflict(err error, ae **agentboard.APIError) bool {
	return errors.As(err, ae) && (*ae).Status == 409
}
