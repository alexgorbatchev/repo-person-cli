package main

import (
	"fmt"

	cobrahelptree "github.com/alexgorbatchev/cobra-help-tree/v2"
	"github.com/alexgorbatchev/repo-person-cli/internal/agent"
	"github.com/spf13/cobra"
)

var techCatalog = cobrahelptree.TechCatalog{
	"repo-person": {
		Summary:     "Synchronize repositories and finish quiet changes with bounded agent assistance",
		Description: "Synchronize repositories and finish quiet changes with bounded agent assistance - command-line interface.",
	},
}

func setupHelp(cmd *cobra.Command) {
	_ = cobrahelptree.SetupWithOptions(cmd, cobrahelptree.HelpOptions{
		Catalog: techCatalog,
	})
	help := cmd.HelpFunc()
	cmd.SetHelpFunc(func(c *cobra.Command, args []string) {
		if agent.IsAgentMode() {
			if _, err := fmt.Fprintln(c.OutOrStdout(), "ALERT: Agents must read `AGENT=1 repo-person skill` before using this tool."); err != nil {
				c.PrintErrln(err)
				return
			}
		}
		help(c, args)
	})
}
