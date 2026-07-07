package summarize

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// LocalCompleter is a Completer backed by a local command: it runs the command,
// writes the prompt to its stdin, and returns its trimmed stdout as the
// completion. It wraps any local model runner (an Ollama/llm-style CLI) without
// committing to an HTTP request schema.
type LocalCompleter struct {
	command string
	args    []string
	// run executes the command with the prompt on stdin and returns stdout. It is
	// a field so tests can substitute a runner without a real binary.
	run func(ctx context.Context, command string, args []string, stdin string) (string, error)
}

// NewLocalCompleter builds a LocalCompleter from cfg.
func NewLocalCompleter(cfg LocalConfig) *LocalCompleter {
	return &LocalCompleter{command: cfg.Command, args: cfg.Args, run: runCommand}
}

// Complete runs the configured command with prompt on stdin and returns its
// stdout. An empty command is a configuration error surfaced here rather than at
// startup, since the summarizer is constructed lazily.
func (c *LocalCompleter) Complete(ctx context.Context, prompt string) (string, error) {
	if c.command == "" {
		return "", fmt.Errorf("summarize: local backend has no command configured")
	}
	out, err := c.run(ctx, c.command, c.args, prompt)
	if err != nil {
		return "", fmt.Errorf("summarize: local command %q: %w", c.command, err)
	}
	return strings.TrimSpace(out), nil
}

// runCommand executes command with args, feeding stdin, and returns stdout. A
// non-zero exit includes any stderr in the error for diagnosis.
func runCommand(ctx context.Context, command string, args []string, stdin string) (string, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("%w: %s", err, msg)
		}
		return "", err
	}
	return stdout.String(), nil
}
