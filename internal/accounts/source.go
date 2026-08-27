// Package accounts provides per-mailbox access. Fastmail is spoken over JMAP
// directly; Gmail is accessed through the already-authenticated gog CLI so we
// reuse OpenClaw's OAuth token without copying it into the repository.
package accounts

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

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
	// UnsubscribeInfo returns the parsed List-Unsubscribe header(s) for a thread,
	// used to actually unsubscribe from a mailing list.
	UnsubscribeInfo(ctx context.Context, thread *model.Thread) (*model.UnsubscribeInfo, error)
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

// runGog runs the gog CLI capturing stdout and stderr separately. It returns the
// command's stdout (the JSON/TSV), and on a non-zero exit an error whose message
// carries stderr — where gog prints human messages (e.g. "No auth", the one-time
// "Note: Using direct access token" line).
func runGog(ctx context.Context, gogBin string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, gogBin, args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.String(), fmt.Errorf("%w: %s", err, msg)
	}
	return stdout.String(), nil
}

// execGog runs gog returning stdout only (so stderr notes never corrupt the JSON).
func execGog(ctx context.Context, gogBin string, args ...string) (string, error) {
	return runGog(ctx, gogBin, nil, args...)
}

// execGogEnv is execGog with an extra env entry (used to inject GOG_ACCESS_TOKEN).
func execGogEnv(ctx context.Context, gogBin string, env []string, args ...string) (string, error) {
	return runGog(ctx, gogBin, env, args...)
}

// execGogAsUser runs gog in the user's login (GUI) session via `launchctl asuser`
// on macOS, so it can read the login keychain token even from SSH. This mirrors
// OpenClaw, whose launchd-spawned gog has keychain access. On non-macOS it falls
// back to running gog directly.
func execGogAsUser(ctx context.Context, gogBin string, args ...string) (string, error) {
	if runtime.GOOS != "darwin" {
		return execGog(ctx, gogBin, args...)
	}
	cmdArgs := append([]string{"asuser", strconv.Itoa(os.Getuid()), gogBin}, args...)
	return runGog(ctx, "launchctl", nil, cmdArgs...)
}
