package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/jordi9/skillsrc/internal/skillsrc"
)

func main() {
	options, err := skillsrc.DefaultCLIOptions()
	if err != nil {
		fmt.Fprintf(os.Stderr, "skillsrc: determine default paths: %v\n", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(skillsrc.RunCLI(ctx, os.Args[1:], options))
}
