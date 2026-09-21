// Quickstart: embed an agentboard server in your own Go program.
//
//	go run ./examples/quickstart      # then open http://127.0.0.1:7878
//
// The board is saved to board.json in the current directory.
package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"

	"github.com/JiaBao-do/agentboard"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	board, err := agentboard.Open(agentboard.Options{Store: agentboard.NewFileStore("board.json")})
	if err != nil {
		log.Fatal(err)
	}
	defer board.Close(context.Background()) // flush the save queue
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:7878")
	if err != nil {
		log.Fatal(err)
	}
	log.Println("board on http://" + ln.Addr().String())
	log.Fatal(agentboard.NewServer(agentboard.ServerOptions{Board: board}).Serve(ctx, ln))
}
