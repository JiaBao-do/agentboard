// Command buildui rebuilds the WebAssembly UI into internal/webui/dist:
// app.wasm from cmd/agentboard-ui, and the wasm_exec.js that belongs to the
// Go toolchain doing the build (the two must come from the same release).
//
// Run it with "go generate ./internal/webui" or "go run ./internal/buildui".
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "buildui:", err)
		os.Exit(1)
	}
}

func output(name string, args ...string) (string, error) {
	out, err := exec.CommandContext(context.Background(), name, args...).Output()
	return strings.TrimSpace(string(out)), err
}

func run() error {
	root, err := output("go", "list", "-m", "-f", "{{.Dir}}")
	if err != nil {
		return fmt.Errorf("locating module root: %w", err)
	}
	goroot, err := output("go", "env", "GOROOT")
	if err != nil {
		return err
	}
	dist := filepath.Join(root, "internal", "webui", "dist")

	wasm := filepath.Join(dist, "app.wasm")
	cmd := exec.CommandContext(context.Background(), "go", "build", "-trimpath", "-ldflags=-s -w", "-o", wasm, "./cmd/agentboard-ui")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm", "CGO_ENABLED=0")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("building wasm: %w", err)
	}

	var src string
	for _, dir := range []string{"lib/wasm", "misc/wasm"} { // moved in Go 1.24
		p := filepath.Join(goroot, filepath.FromSlash(dir), "wasm_exec.js")
		if _, err := os.Stat(p); err == nil {
			src = p
			break
		}
	}
	if src == "" {
		return fmt.Errorf("wasm_exec.js not found under %s", goroot)
	}
	if err := copyFile(src, filepath.Join(dist, "wasm_exec.js")); err != nil {
		return err
	}
	fi, err := os.Stat(wasm)
	if err != nil {
		return err
	}
	ver, _ := output("go", "env", "GOVERSION")
	fmt.Printf("buildui: %s (%d bytes) built with %s\n", wasm, fi.Size(), ver)
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
