// Command portby is both the Portby daemon (`portby daemon`) and its CLI.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/mrsafalpiya/portby.dev/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "portby: %v\n", err)
		os.Exit(1)
	}
	os.Exit(cli.Run(ctx, cli.Env{
		Args:     os.Args[1:],
		Dir:      dir,
		Getenv:   os.Getenv,
		Stdin:    os.Stdin,
		Terminal: isTerminal(os.Stdin),
		Stdout:   os.Stdout,
		Stderr:   os.Stderr,
	}))
}

// isTerminal reports whether f is a terminal (a character device), so a
// person may be there to answer questions.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
