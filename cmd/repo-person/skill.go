package main

import (
	"github.com/spf13/cobra"
	"io"
)

const skillContent = `Install the repo-person skill using the Skills CLI:

  npx skills add alexgorbatchev/repo-person-cli --skill repo-person

Skill:

  https://github.com/alexgorbatchev/repo-person-cli/tree/main/skills/repo-person
`

func newSkillCommand() *cobra.Command {
	return &cobra.Command{Use: "skill", Short: "Show agent skill installation instructions", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		_, err := io.WriteString(cmd.OutOrStdout(), skillContent)
		return err
	}}
}
