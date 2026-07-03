package cmd

import (
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.kenn.io/kwt/internal/shell"
)

var completionCmd = &cobra.Command{
	Use:   "completion [bash|zsh|fish|powershell]",
	Short: "Generate shell completion and integration scripts",
	Long: `Generate shell completion scripts with optional shell integration.

When cd.launch_shell is set to false in your config, this also generates
a shell wrapper function that enables 'kwt cd' to change directory
in the current shell without launching a new shell.

  # bash (~/.bashrc)
  source <(kwt completion bash)

  # zsh (~/.zshrc)
  source <(kwt completion zsh)

  # fish (~/.config/fish/config.fish)
  kwt completion fish | source

  # powershell
  kwt completion powershell | Out-String | Invoke-Expression`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

var completionBashCmd = &cobra.Command{
	Use:   "bash",
	Short: "Generate bash completion script",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := cmd.Root().GenBashCompletionV2(cmd.OutOrStdout(), true); err != nil {
			return err
		}
		if !viper.GetBool("cd.launch_shell") {
			return shell.WriteWrapper(cmd.OutOrStdout(), "bash", shell.TemplateData{CommandName: "kwt"})
		}
		return nil
	},
}

var completionZshCmd = &cobra.Command{
	Use:   "zsh",
	Short: "Generate zsh completion script",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := cmd.Root().GenZshCompletion(cmd.OutOrStdout()); err != nil {
			return err
		}
		if !viper.GetBool("cd.launch_shell") {
			return shell.WriteWrapper(cmd.OutOrStdout(), "zsh", shell.TemplateData{CommandName: "kwt"})
		}
		return nil
	},
}

var completionFishCmd = &cobra.Command{
	Use:   "fish",
	Short: "Generate fish completion script",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := cmd.Root().GenFishCompletion(cmd.OutOrStdout(), true); err != nil {
			return err
		}
		if !viper.GetBool("cd.launch_shell") {
			return shell.WriteWrapper(cmd.OutOrStdout(), "fish", shell.TemplateData{CommandName: "kwt"})
		}
		return nil
	},
}

var completionPowershellCmd = &cobra.Command{
	Use:   "powershell",
	Short: "Generate powershell completion script",
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Root().GenPowerShellCompletionWithDesc(cmd.OutOrStdout())
	},
}

func init() {
	completionCmd.AddCommand(completionBashCmd)
	completionCmd.AddCommand(completionZshCmd)
	completionCmd.AddCommand(completionFishCmd)
	completionCmd.AddCommand(completionPowershellCmd)
	rootCmd.AddCommand(completionCmd)
}
