// Package cmd provides CLI commands for the kwt application.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"sync/atomic"
	"syscall"

	"github.com/spf13/cobra"
	"go.kenn.io/kwt/internal/config"
	"golang.org/x/term"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

var (
	mergeCwdLocal = config.MergeCwdLocal
	configInitErr error

	stdinIsTerminal = func() bool {
		return term.IsTerminal(int(os.Stdin.Fd()))
	}
	stdoutIsTerminal = func() bool {
		return term.IsTerminal(int(os.Stdout.Fd()))
	}
	runRootTUI = runTUI
)

// rootCmd represents the base command when called without any subcommands.
var rootCmd = &cobra.Command{
	Use:   "kwt",
	Short: "Git worktree manager",
	Long: `kwt is a CLI tool for efficiently managing Git worktrees.

Like how 'ghq' manages repository clones, kwt provides intuitive 
operations for creating, switching, and deleting worktrees using 
a fuzzy finder interface.`,
	Version: getVersionString(),
	Args:    cobra.NoArgs,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		if err := requireConfigInitialization(); err != nil {
			return err
		}
		if cmd == cmd.Root() {
			return nil
		}
		return mergeCwdLocal()
	},
	RunE: runRoot,
}

// Execute adds all child commands to the root command and sets flags appropriately.
func Execute() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var signalExitCode atomic.Int32
	go func() {
		select {
		case received := <-signals:
			switch received {
			case os.Interrupt:
				signalExitCode.Store(130)
			case syscall.SIGTERM:
				signalExitCode.Store(143)
			}
			signal.Stop(signals)
			cancel()
		case <-done:
		}
	}()
	defer func() {
		close(done)
		signal.Stop(signals)
		cancel()
	}()
	err := rootCmd.ExecuteContext(ctx)
	exitCode := 0
	if err != nil {
		exitCode = 1
		var coded interface{ ExitCode() int }
		if errors.As(err, &coded) {
			exitCode = coded.ExitCode()
		}
	}
	if code := signalExitCode.Load(); code != 0 {
		exitCode = int(code)
	}
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.CompletionOptions.DisableDefaultCmd = true
}

func runRoot(cmd *cobra.Command, args []string) error {
	if stdinIsTerminal() && stdoutIsTerminal() {
		return runRootTUI(cmd, args)
	}
	return cmd.Help()
}

// initConfig reads in config file and ENV variables if set. Cobra's
// initializer callback cannot return an error, so command pre-run hooks
// propagate the stored failure without terminating the process directly.
func initConfig() {
	configInitErr = config.Init()
}

func requireConfigInitialization() error {
	if configInitErr != nil {
		return fmt.Errorf("initialize configuration: %w", configInitErr)
	}
	return nil
}

// getVersionString returns a formatted version string using build info
func getVersionString() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return fmt.Sprintf("%s (commit: %s, built: %s)", version, commit, date)
	}

	// Extract version information from build info
	buildVersion := version
	buildCommit := commit
	buildDate := date

	// Try to get version from module
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		buildVersion = info.Main.Version
	}

	// Try to get commit and date from VCS settings
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			if setting.Value != "" {
				buildCommit = setting.Value
				if len(buildCommit) > 7 {
					buildCommit = buildCommit[:7]
				}
			}
		case "vcs.time":
			if setting.Value != "" {
				buildDate = setting.Value
			}
		}
	}

	return fmt.Sprintf("%s (commit: %s, built: %s)", buildVersion, buildCommit, buildDate)
}
