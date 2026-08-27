package accounts

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jokull/sift/internal/config"
	"github.com/jokull/sift/internal/model"
)

type gmailSource struct {
	cfg *config.GmailConfig
}

func newGmail(cfg *config.GmailConfig) (*gmailSource, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil gmail config")
	}
	return &gmailSource{cfg: cfg}, nil
}

func (g *gmailSource) Account() model.Account { return model.AccountGmail }

// gog runs the CLI, injecting GOG_ACCESS_TOKEN when one is configured (for SSH
// sessions where the gog keychain token is unavailable).
func (g *gmailSource) gog(ctx context.Context, args ...string) (string, error) {
	if g.cfg.AccessToken != "" {
		return execGogEnv(ctx, g.cfg.GogBin, []string{"GOG_ACCESS_TOKEN=" + g.cfg.AccessToken}, args...)
	}
	return execGog(ctx, g.cfg.GogBin, args...)
}

// gmailThread mirrors the gog `gmail search` JSON rows.
type gmailThread struct {
	Date            string   `json:"date"`
	From            string   `json:"from"`
	ID              string   `json:"id"`
	InternalDateIso string   `json:"internalDateIso"`
	Labels          []string `json:"labels"`
	MessageCount    int      `json:"messageCount"`
	Subject         string   `json:"subject"`
}

func (g *gmailSource) ListThreads(ctx context.Context, limit int) ([]*model.Thread, error) {
	if limit <= 0 {
		limit = 60
	}
	out, err := g.gog(ctx,
		"gmail", "search", "in:inbox",
		"-a", g.cfg.Account,
		"--max="+fmt.Sprint(limit),
		"--results-only", "-j", "--no-input",
	)
	if err != nil {
		return nil, gogHint("gmail search", err, out)
	}
	var rows []gmailThread
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		return nil, fmt.Errorf("parse gog search output: %w\n%s", err, truncate(out, 400))
	}

	threads := make([]*model.Thread, 0, len(rows))
	for _, r := range rows {
		name, email := splitFrom(r.From)
		t := &model.Thread{
			ID:           r.ID,
			Account:      model.AccountGmail,
			Subject:      r.Subject,
			FromName:     name,
			FromEmail:    strings.ToLower(email),
			Date:         parseInternalDate(r.InternalDateIso),
			MessageCount: r.MessageCount,
			Labels:       r.Labels,
		}
		if t.Date.IsZero() {
			if d, err := time.ParseInLocation("2006-01-02 15:04", r.Date, time.Local); err == nil {
				t.Date = d.UTC()
			}
		}
		t.Unread = countLabel(r.Labels, "UNREAD")
		threads = append(threads, t)
	}
	sortThreadsNewest(threads)
	if len(threads) > limit {
		threads = threads[:limit]
	}
	return threads, nil
}

func (g *gmailSource) Apply(ctx context.Context, threads []*model.Thread, action model.Action) error {
	if len(threads) == 0 {
		return nil
	}
	for _, th := range threads {
		args := []string{"gmail", "thread", "modify", th.ID,
			"-a", g.cfg.Account, "--no-input"}
		switch action {
		case model.ActionReceipts:
			args = append(args, "--add="+g.cfg.ReceiptsLabel, "--remove=INBOX")
		case model.ActionReading:
			args = append(args, "--add="+g.cfg.ReadingLabel, "--remove=INBOX")
		case model.ActionArchive, model.ActionUnsubscribe:
			args = append(args, "--remove=INBOX")
		default:
			return fmt.Errorf("gmail: unsupported action %q", action)
		}
		out, err := g.gog(ctx, args...)
		if err != nil {
			return classifyGogError(fmt.Sprintf("gmail modify %s", th.ID), err, out)
		}
	}
	return nil
}

func (g *gmailSource) EnsureFolders(ctx context.Context) error {
	out, err := g.gog(ctx,
		"gmail", "labels", "list",
		"-a", g.cfg.Account, "--results-only", "-j", "--no-input")
	if err != nil {
		return classifyGogError("gmail labels list", err, out)
	}
	var labels []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(out), &labels); err != nil {
		return fmt.Errorf("parse gog labels: %w", err)
	}
	have := map[string]bool{}
	for _, l := range labels {
		have[l.Name] = true
		if l.Name == "Receipts" && g.cfg.ReceiptsLabel == "" {
			g.cfg.ReceiptsLabel = l.Name
		}
		if l.Name == "Reading" && g.cfg.ReadingLabel == "" {
			g.cfg.ReadingLabel = l.Name
		}
	}
	for _, want := range []string{g.cfg.ReceiptsLabel, g.cfg.ReadingLabel} {
		if want == "" || have[want] {
			continue
		}
		if _, err := g.gog(ctx,
			"gmail", "labels", "create", want,
			"-a", g.cfg.Account, "--no-input"); err != nil {
			return fmt.Errorf("create gmail label %q: %w", want, err)
		}
		have[want] = true
	}
	return nil
}

var bracketRe = regexp.MustCompile(`(?s)^(.*?)\s*<([^<>]+)>$`)

func splitFrom(s string) (name, email string) {
	s = strings.TrimSpace(s)
	if m := bracketRe.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
	}
	return "", s
}

func parseInternalDate(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05Z07:00", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func countLabel(labels []string, target string) int {
	n := 0
	for _, l := range labels {
		if strings.EqualFold(l, target) {
			n++
		}
	}
	return n
}

func classifyGogError(op string, err error, out string) error {
	msg := strings.TrimSpace(out)
	if msg == "" {
		return fmt.Errorf("%s: %w", op, err)
	}
	return fmt.Errorf("%s: %w (%s)", op, err, truncate(msg, 300))
}

// gogHint wraps a gog failure with SSH remediation when the cause looks like a
// keychain/credential access problem, which is common over SSH.
func gogHint(op string, err error, out string) error {
	base := classifyGogError(op, err, out)
	msg := strings.ToLower(out + " " + err.Error())
	if strings.Contains(msg, "keychain") || strings.Contains(msg, "credential") ||
		strings.Contains(msg, "interaction") || strings.Contains(msg, "keyring") {
		return fmt.Errorf("%w — gog couldn't read its Gmail token from the macOS keychain. Over SSH, set [gmail] access_token (a ~1h token from `gog` in your desktop session) or run sift from a GUI session.", base)
	}
	return base
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
