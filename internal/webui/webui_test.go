package webui_test

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"github.com/JiaBao-do/agentboard/internal/webui"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := fs.ReadFile(webui.FS(), name)
	if err != nil {
		t.Fatalf("embedded %s: %v", name, err)
	}
	if len(b) == 0 {
		t.Fatalf("embedded %s is empty", name)
	}
	return b
}

func TestEmbeddedFiles(t *testing.T) {
	for _, name := range []string{"index.html", "boot.js", "style.css", "wasm_exec.js", "app.wasm"} {
		read(t, name)
	}
	if w := read(t, "app.wasm"); string(w[:4]) != "\x00asm" {
		t.Fatalf("app.wasm does not start with the WebAssembly magic: % x", w[:4])
	}
	if !strings.Contains(string(read(t, "wasm_exec.js")), "Go") {
		t.Fatal("wasm_exec.js does not look like Go's loader")
	}
}

// The UI must be fully self-contained: no CDN, font host or script from
// another origin, or the single-binary promise is broken.
func TestNoExternalReferences(t *testing.T) {
	remote := regexp.MustCompile(`(?i)(https?:)?//[a-z0-9.-]+\.[a-z]{2,}`)
	for _, name := range []string{"index.html", "boot.js", "style.css"} {
		for _, m := range remote.FindAllString(string(read(t, name)), -1) {
			t.Errorf("%s references %q; the UI must not load anything from the network", name, m)
		}
	}
	css := string(read(t, "style.css"))
	if strings.Contains(css, "@import") || strings.Contains(css, "url(") {
		t.Error("style.css imports or links external resources")
	}
	html := string(read(t, "index.html"))
	if strings.Contains(html, "<script>") || strings.Contains(html, "onclick=") {
		t.Error("index.html contains inline script, which the CSP forbids")
	}
}
