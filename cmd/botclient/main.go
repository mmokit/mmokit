package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	addr := flag.String("addr", "localhost:8080", "server WebSocket address (host:port, http(s)://…, or ws(s)://…)")
	scenario := flag.String("scenario", "duel", "scenario to run (duel, miners)")
	count := flag.Int("count", 2, "number of bots to spawn")
	flag.Parse()

	switch *scenario {
	case "duel":
		runDuel(*addr, *count)
	case "miners":
		runMiners(*addr, *count)
	default:
		fmt.Fprintf(os.Stderr, "unknown scenario: %s\n", *scenario)
		os.Exit(1)
	}
}
