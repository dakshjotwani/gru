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
		fmt.Fprintf(os.Stderr, "Usage: gru <server|init> [args...]\n")
		return nil
	}
	switch args[0] {
	case "server":
		return runServer()
	case "init":
		if err := runInit(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return nil
	default:
		return fmt.Errorf("unknown command: %s", args[0])
	}
}
