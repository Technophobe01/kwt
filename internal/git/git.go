// Package git provides Git operations for the kwt application.
package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Git provides Git command operations.
type Git struct {
	workDir string
}

// New creates a new Git instance.
func New(workDir string) *Git {
	return &Git{
		workDir: workDir,
	}
}

// NewFromCwd creates a new Git instance using the current working directory.
func NewFromCwd() (*Git, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get current directory: %w", err)
	}
	return New(cwd), nil
}

// RunCommand executes a git command with the provided arguments and returns the output.
// The command is executed in the Git instance's working directory if set.
func (g *Git) RunCommand(args ...string) (string, error) {
	return g.run(args...)
}

// RunWithContext executes a git command with context support for cancellation and timeout.
func (g *Git) RunWithContext(ctx context.Context, args ...string) (string, error) {
	return g.runWithContext(ctx, false, args...)
}

// RunNonInteractiveWithContext executes Git with terminal credential prompts
// disabled. Credential helpers may still supply credentials, but Git will
// fail instead of waiting for stdin when none are available.
func (g *Git) RunNonInteractiveWithContext(ctx context.Context, args ...string) (string, error) {
	return g.runWithEnvironmentContext(ctx, NonInteractiveEnvironment(os.Environ()), args...)
}

// RunWithEnvironmentAndDisabledHooks executes Git with an explicit
// environment and a private empty hooks directory. Import paths use this for
// ref mutations so repository hooks cannot observe kwt-owned credentials.
func (g *Git) RunWithEnvironmentAndDisabledHooks(ctx context.Context, environment []string, args ...string) (string, error) {
	hooksDir, err := os.MkdirTemp("", "kwt-empty-hooks-")
	if err != nil {
		return "", fmt.Errorf("create empty Git hooks directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(hooksDir) }()
	args = append([]string{"-c", "core.hooksPath=" + hooksDir}, args...)
	return g.runWithEnvironmentContext(ctx, environment, args...)
}

// run executes a git command.
func (g *Git) run(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if g.workDir != "" {
		cmd.Dir = g.workDir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), stderr.String())
	}

	return stdout.String(), nil
}

// runWithContext executes a git command with context support.
func (g *Git) runWithContext(ctx context.Context, nonInteractive bool, args ...string) (string, error) {
	if nonInteractive {
		return g.runWithEnvironmentContext(ctx, NonInteractiveEnvironment(os.Environ()), args...)
	}
	return g.runWithEnvironmentContext(ctx, nil, args...)
}

func (g *Git) runWithEnvironmentContext(ctx context.Context, environment []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if g.workDir != "" {
		cmd.Dir = g.workDir
	}
	if environment != nil {
		cmd.Env = environment
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), ctx.Err())
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), stderr.String())
	}

	return stdout.String(), nil
}

// NonInteractiveEnvironment disables Git, credential-manager, and OpenSSH
// prompting while preserving the caller's ordinary authentication setup.
func NonInteractiveEnvironment(environment []string) []string {
	sshVariant := environmentValue(environment, "GIT_SSH_VARIANT")
	sshCommand := environmentValue(environment, "GIT_SSH_COMMAND")
	if strings.TrimSpace(sshCommand) == "" {
		if sshExecutable := environmentValue(environment, "GIT_SSH"); strings.TrimSpace(sshExecutable) != "" {
			if variant := strings.ToLower(strings.TrimSpace(sshVariant)); variant == "" || variant == "auto" {
				sshVariant = detectSSHExecutableVariant(sshExecutable)
			}
			sshCommand = shellQuote(sshExecutable)
		}
	}
	if strings.TrimSpace(sshCommand) == "" {
		sshCommand = "ssh"
	}
	sshCommand = nonInteractiveSSHCommand(sshCommand, sshVariant)
	for _, key := range []string{
		"GIT_TERMINAL_PROMPT", "GIT_ASKPASS", "SSH_ASKPASS", "SSH_ASKPASS_REQUIRE",
		"GCM_INTERACTIVE", "GIT_CREDENTIAL_INTERACTIVE", "GIT_SSH_COMMAND",
	} {
		environment = withoutEnvironmentKey(environment, key)
	}
	return append(environment,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		"SSH_ASKPASS_REQUIRE=never",
		"GCM_INTERACTIVE=Never",
		"GIT_CREDENTIAL_INTERACTIVE=never",
		"GIT_SSH_COMMAND="+sshCommand,
	)
}

func nonInteractiveSSHCommand(command, variant string) string {
	variant = strings.ToLower(strings.TrimSpace(variant))
	if variant == "" || variant == "auto" {
		variant = detectSSHVariant(command)
	}
	switch variant {
	case "ssh":
		return command + " -oBatchMode=yes"
	case "plink", "putty", "tortoiseplink":
		return command + " -batch"
	default:
		return command
	}
}

func detectSSHVariant(command string) string {
	executable := leadingShellWord(command)
	if executable == "" {
		return ""
	}
	return detectSSHExecutableVariant(executable)
}

func leadingShellWord(command string) string {
	command = strings.TrimLeft(command, " \t\r\n")
	var word strings.Builder
	var quote byte
	for i := 0; i < len(command); i++ {
		ch := command[i]
		if quote == 0 {
			switch ch {
			case ' ', '\t', '\r', '\n':
				return word.String()
			case '\'', '"':
				quote = ch
			case '\\':
				if i+1 < len(command) {
					next := command[i+1]
					if next == ' ' || next == '\t' || next == '\r' || next == '\n' || next == '\'' || next == '"' || next == '\\' {
						i++
						word.WriteByte(next)
					} else {
						word.WriteByte(ch)
					}
				}
			default:
				word.WriteByte(ch)
			}
			continue
		}
		if ch == quote {
			quote = 0
			continue
		}
		if ch == '\\' && quote == '"' && i+1 < len(command) {
			next := command[i+1]
			switch next {
			case '$', '`', '"', '\\', '\n':
				i++
				word.WriteByte(next)
				continue
			}
		}
		word.WriteByte(ch)
	}
	return word.String()
}

func detectSSHExecutableVariant(executable string) string {
	executable = strings.ReplaceAll(executable, `\`, "/")
	if slash := strings.LastIndexByte(executable, '/'); slash >= 0 {
		executable = executable[slash+1:]
	}
	executable = strings.TrimSuffix(strings.ToLower(executable), ".exe")
	switch executable {
	case "ssh":
		return "ssh"
	case "plink", "putty", "tortoiseplink":
		return executable
	default:
		return ""
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func environmentValue(environment []string, key string) string {
	for _, entry := range environment {
		name, value, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(name, key) {
			return value
		}
	}
	return ""
}

func withoutEnvironmentKey(environment []string, key string) []string {
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.EqualFold(name, key) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}
