// Package accounts provides per-mailbox access. Fastmail is spoken over JMAP
// directly; Gmail is accessed through the already-authenticated gog CLI so we
// reuse OpenClaw's OAuth token without copying it into the repository.
package accounts

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/jokull/sift/internal/config"
	"github.com/jokull/sift/internal/model"
)

// Source is the account-facing contract used by the triage engine.
type Source interface {
	// Account identifies the mailbox.
	Account() model.Account
	// ListThreads returns conversations sitting in the inbox, newest first,
	// with metadata sufficient to classify and act on them.
	ListThreads(ctx context.Context, limit int) ([]*model.Thread, error)
	// Apply moves the given threads according to action. Threads are handled as
	// whole conversations (every message in the thread).
	Apply(ctx context.Context, threads []*model.Thread, action model.Action) error
	// EnsureFolders creates any target labels/folders (Receipts/Reading) that do
	// not yet exist, so actions have a destination.
	EnsureFolders(ctx context.Context) error
}

// New builds the configured sources. At least one account must be present.
func New(cfg *config.Config) (map[model.Account]Source, error) {
	srcs := map[model.Account]Source{}
	if cfg.Fastmail != nil {
		s, err := newFastmail(cfg.Fastmail)
		if err != nil {
			return nil, fmt.Errorf("fastmail: %w", err)
		}
		srcs[model.AccountFastmail] = s
	}
	if cfg.Gmail != nil {
		g, err := newGmail(cfg.Gmail)
		if err != nil {
			return nil, fmt.Errorf("gmail: %w", err)
		}
		srcs[model.AccountGmail] = g
	}
	if len(srcs) == 0 {
		return nil, fmt.Errorf("no mail accounts configured")
	}
	return srcs, nil
}

// execGog runs the gog CLI returning combined output and trimming whitespace.
func execGog(ctx context.Context, gogBin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, gogBin, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// execGogEnv is execGog with an extra env entry (used to inject GOG_ACCESS_TOKEN).
func execGogEnv(ctx context.Context, gogBin string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, gogBin, args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}
