//go:build !(js && wasm)

// Command agentboard-ui is the browser UI. It only builds for
// GOOS=js GOARCH=wasm; run "go generate ./internal/webui" to build it. This
// stub keeps "go build ./..." and "go vet ./..." working on other platforms.
package main

func main() {}
