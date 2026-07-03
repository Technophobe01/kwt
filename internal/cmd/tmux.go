package cmd

import (
	"github.com/spf13/cobra"
)

var tmuxCmd = &cobra.Command{
	Use:   "tmux",
	Short: "Manage tmux sessions for long-running processes",
	Long: `Manage tmux sessions for long-running processes with persistence and monitoring.

This command provides session management capabilities for any long-running commands 
or processes, enabling process persistence, monitoring, and history management.`,
	Example: `  # List active tmux sessions
  kwt tmux list

  # Run command in new tmux session
  kwt tmux run "npm run dev"

  # Attach to session
  kwt tmux attach auth

  # Terminate session
  kwt tmux kill auth`,
}

func init() {
	rootCmd.AddCommand(tmuxCmd)
}
