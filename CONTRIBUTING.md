# Contributing

Thanks for helping. This project is small on purpose: standard library only, one static binary.

## Setup

```sh
git clone https://github.com/JiaBao-do/agentboard
cd agentboard
git config core.hooksPath .githooks   # enable the push gate
go test -race ./...
```

## The push gate

`.githooks/pre-push` checks the exact commit you push, from a clean checkout: `gofmt`, `go mod tidy`,
`go vet`, `go test -race -shuffle=on`, `go build`, the WebAssembly vet/build and `golangci-lint` when
installed. Do not bypass it with `--no-verify`; fix the failure instead.

## Rules

- Every behavior change comes with a test; every bug fix with a regression test.
- Standard library only. A pull request that adds a `require` line will not be merged.
- Exported identifiers need doc comments.
- If you change `model/`, `internal/view` or `cmd/agentboard-ui`, run `go generate ./internal/webui` and commit
  the rebuilt `app.wasm` and `wasm_exec.js`.
- Conventional Commits (`feat:`, `fix:`, `docs:`, `test:`, `chore:`), one logical change per commit.
- Keep the portability guarantees listed in `CLAUDE.md` intact.

## Reporting bugs and security issues

Bugs: open an issue with the steps to reproduce. Security problems: see [SECURITY.md](SECURITY.md).
