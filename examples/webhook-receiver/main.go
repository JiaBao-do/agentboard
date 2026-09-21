// webhook-receiver: a tiny consumer of agentboard's generic outgoing webhook.
// It verifies the HMAC signature on the raw body before trusting anything.
//
//	go run ./examples/webhook-receiver                 # listens on 127.0.0.1:9000
//	agentboard serve -webhook http://127.0.0.1:9000 -webhook-secret s3cret
//
// Set AGENTBOARD_WEBHOOK_SECRET to the same secret. Delivery is best effort,
// at most once: treat events as hints and re-read the API for the truth (the
// task in the payload is its state at delivery time, not at event time).
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	"github.com/JiaBao-do/agentboard"
)

func main() {
	secret := os.Getenv("AGENTBOARD_WEBHOOK_SECRET")
	log.Println("webhook receiver on http://127.0.0.1:9000")
	log.Fatal(http.ListenAndServe("127.0.0.1:9000", handler(secret, os.Stdout)))
}

// handler prints one line per verified event.
func handler(secret string, w io.Writer) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(rw, r.Body, 1<<20))
		if err != nil {
			http.Error(rw, "bad body", http.StatusBadRequest)
			return
		}
		// Verify the signature over the exact bytes received, in constant time.
		if secret != "" && !agentboard.VerifyWebhookSignature(secret, body, r.Header.Get("X-Agentboard-Signature")) {
			http.Error(rw, "bad signature", http.StatusUnauthorized)
			return
		}
		var p agentboard.WebhookPayload
		if err := json.Unmarshal(body, &p); err != nil {
			http.Error(rw, "bad json", http.StatusBadRequest)
			return
		}
		if p.Task != nil {
			fmt.Fprintf(w, "event=%s task=%s status=%s agent=%s\n", p.Event.Type, p.Task.ID, p.Task.Status, p.Task.Assignee)
		} else {
			fmt.Fprintf(w, "event=%s\n", p.Event.Type)
		}
		rw.WriteHeader(http.StatusNoContent)
	})
}
