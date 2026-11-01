package main

import (
	cobrahelptree "github.com/alexgorbatchev/cobra-help-tree/v4"
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
		Skill:   cobrahelptree.SkillSource{Repository: "alexgorbatchev/repo-person-cli", Branch: "main"},
	})
}
