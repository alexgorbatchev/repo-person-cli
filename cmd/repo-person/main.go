package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/alexgorbatchev/repo-person-cli/internal/agent"
	"github.com/alexgorbatchev/repo-person-cli/internal/display"
	"github.com/spf13/cobra"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	cmd := newRootCommand()
	cmd.SetContext(ctx)
	cmd.SetOut(display.New(os.Stdout))
	cmd.SetErr(display.New(os.Stderr))
	os.Exit(run(cmd))
}
func run(cmd *cobra.Command) int {
	if err := cmd.Execute(); err != nil {
		prefix := "[ERROR]"
		if agent.IsAgentMode() {
			prefix = "ERR:"
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "%s %s\n", prefix, display.Path(err.Error()))
		return 1
	}
	return 0
}
