package main

import (
	"fmt"
	"os"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Println("usage: gru <command>")
		fmt.Println("commands: server")
		return nil
	}
	switch args[0] {
	case "server":
		return runServer()
	default:
		return fmt.Errorf("unknown command: %s", args[0])
	}
}
