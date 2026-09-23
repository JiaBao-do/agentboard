// auth-demo: registers a human account, logs in, checks "who am I", logs
// out, and shows that logging out really ends the session server-side (a
// replayed session is refused, not just forgotten). It also confirms that
// none of this is required for the existing agent workflow: a task write
// authenticated only by X-Agent-Name (no login at all) still succeeds,
// exactly as before AGENTBOARD-8. See docs/PITFALLS.md and the README
// "Things to care about" for the security notes behind these calls
// (password hashing, session cookies, the plain-HTTP/TLS caveat).
//
//	go run ./examples/auth-demo                       # starts its own throwaway board
//	go run ./examples/auth-demo -url http://127.0.0.1:7878
//
// The output is deterministic (see expected_output.txt); the test in this
// directory checks it. curl equivalents of every call here are in the
// package doc comment of auth_server.go and in docs/PITFALLS.md.
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
	if err := run(ctx, os.Stdout, *url); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, w io.Writer, url string) error {
	const email = "dev@example.com" // example.com is reserved for documentation (RFC 2606); never a real domain
	const password = "correct horse battery staple"

	// A fresh Client per "browser": NewClient sets up a cookie jar, so a
	// session started by Register or Login survives later calls on the
	// *same* Client, exactly like a browser tab keeps its cookie.
	c := agentboard.NewClient(url, "")

	u, err := c.Register(ctx, email, password)
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	fmt.Fprintf(w, "registered %s\n", u.Email)

	// Register also logs in (see auth_server.go): no separate Login call
	// needed right after creating the account.
	me, err := c.Me(ctx)
	if err != nil {
		return fmt.Errorf("me after register: %w", err)
	}
	fmt.Fprintf(w, "me: %s (logged in)\n", me.Email)

	if err := c.Logout(ctx); err != nil {
		return fmt.Errorf("logout: %w", err)
	}
	fmt.Fprintln(w, "logged out")

	if _, err := c.Me(ctx); isUnauthorized(err) {
		fmt.Fprintln(w, "me: not logged in (as expected)")
	} else {
		return fmt.Errorf("me after logout: want 401, got %w", err)
	}

	// A separate Client (its own cookie jar, like a different browser or a
	// different machine) must log in explicitly; nothing carries over.
	other := agentboard.NewClient(url, "")
	if _, err := other.Login(ctx, email, "wrong password"); isUnauthorized(err) {
		fmt.Fprintln(w, "wrong password rejected")
	} else {
		return fmt.Errorf("login with wrong password: want 401, got %w", err)
	}
	loggedIn, err := other.Login(ctx, email, password)
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	fmt.Fprintf(w, "logged in again: %s\n", loggedIn.Email)

	// None of the above was required: the existing, credential-less Agent
	// workflow (attribution via X-Agent-Name, no session, no login) keeps
	// working exactly as it did before user accounts existed.
	agentClient := agentboard.NewClient(url, "")
	agentClient.Agent = "example-agent"
	if _, err := agentClient.CreateProject(ctx, agentboard.ProjectRequest{Key: "DEMO", Name: "Demo project"}); err != nil {
		var ae *agentboard.APIError
		if !errors.As(err, &ae) || ae.Status != 409 {
			return fmt.Errorf("create project as an unauthenticated agent: %w", err)
		}
	}
	task, err := agentClient.AddTask(ctx, agentboard.NewTask{Project: "DEMO", Title: "Ship AGENTBOARD-8"})
	if err != nil {
		return fmt.Errorf("add task as an unauthenticated agent: %w", err)
	}
	fmt.Fprintf(w, "task created without logging in: %s\n", task.ID)
	return nil
}

func isUnauthorized(err error) bool {
	var ae *agentboard.APIError
	return errors.As(err, &ae) && ae.Status == 401
}
