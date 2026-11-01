package main

import (
	"fmt"
	"github.com/alexgorbatchev/repo-person-cli/internal/maintenance"
	"github.com/spf13/cobra"
)

var version = "0.1.0"

func newRootCommand() *cobra.Command {
	root := &cobra.Command{Use: "repo-person", Short: "Synchronize repositories and finish quiet changes", Version: version, SilenceUsage: true, SilenceErrors: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	root.SetVersionTemplate("{{.Version}}\n")
	var repo string
	root.PersistentFlags().StringVarP(&repo, "repo", "C", ".", "Repository directory")
	for _, name := range []string{"init", "enable", "disable", "run", "retry", "status", "logs", "fingerprint"} {
		descriptions := map[string]string{"init": "Create repo-person.toml", "enable": "Register this repository on this machine", "disable": "Remove this repository from the machine registry", "run": "Synchronize, sweep quiet changes, and synchronize again", "retry": "Run again without the failure cooldown", "status": "Show eligibility and the latest attempt", "logs": "Print the latest maintenance log", "fingerprint": "Print a fingerprint of pending changes"}
		cmd := &cobra.Command{Use: name, Short: descriptions[name], Args: cobra.NoArgs}
		var all bool
		if name == "run" {
			cmd.Flags().BoolVar(&all, "all", false, "Maintain registered repositories with up to three workers")
		}
		cmd.RunE = func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			switch name {
			case "init":
				return maintenance.Init(ctx, repo)
			case "enable", "disable":
				return maintenance.Register(ctx, repo, name == "enable")
			case "fingerprint":
				value, err := maintenance.Fingerprint(ctx, repo)
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(out, value)
				return err
			}
			if all {
				return maintenance.RunAll(ctx, out)
			}
			c, err := maintenance.Load(ctx, repo)
			if err != nil {
				return err
			}
			switch name {
			case "run", "retry":
				return maintenance.Run(ctx, c, name == "retry", out)
			case "status":
				return maintenance.Status(ctx, c, out)
			case "logs":
				return maintenance.Logs(c, out)
			}
			return nil
		}
		root.AddCommand(cmd)
	}
	root.AddCommand(newSkillCommand())
	setupHelp(root)
	return root
}
