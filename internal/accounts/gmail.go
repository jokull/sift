package accounts

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jokull/sift/internal/config"
	"github.com/jokull/sift/internal/gmailauth"
	"github.com/jokull/sift/internal/gogd"
	"github.com/jokull/sift/internal/model"
	"github.com/jokull/sift/internal/unsub"
)

type gmailSource struct {
	cfg *config.GmailConfig

	haveAuth      bool
	authTok       string
	authErr       error
	bridgeChecked bool
	bridgeOK      bool
}

func newGmail(cfg *config.GmailConfig) (*gmailSource, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil gmail config")
	}
	return &gmailSource{cfg: cfg}, nil
}

func (g *gmailSource) Account() model.Account { return model.AccountGmail }

// ensureToken resolves a Gmail access token from config or mints one from a
// service account / refresh token, so gog can run over SSH without the macOS
// keychain. An empty token means "let gog use its own keychain token".
func (g *gmailSource) ensureToken(ctx context.Context) (string, error) {
	if g.haveAuth {
		return g.authTok, g.authErr
	}
	defer func() { g.haveAuth = true }()

	if tok := strings.TrimSpace(g.cfg.AccessToken); tok != "" {
		g.authTok, g.authErr = tok, nil
		return tok, nil
	}
	if sa := strings.TrimSpace(g.cfg.ServiceAccount); sa != "" {
		key, err := gmailauth.ReadServiceAccountFile(sa)
		if err != nil {
			g.authErr = fmt.Errorf("read service account %q: %w", sa, err)
			return "", g.authErr
		}
		tok, err := gmailauth.NewServiceAccountToken(ctx, key, g.cfg.Account)
		g.authTok, g.authErr = tok, err
		return g.authTok, g.authErr
	}
	if rt := strings.TrimSpace(g.cfg.RefreshToken); rt != "" && g.cfg.ClientID != "" {
		tok, err := gmailauth.RefreshAccessToken(ctx, g.cfg.ClientID, g.cfg.ClientSecret, rt)
		g.authTok, g.authErr = tok, err
		return g.authTok, g.authErr
	}
	g.authTok, g.authErr = "", nil
	return "", nil
}

// gog runs gog for a Gmail operation, trying (in order): the headless gog
// bridge (a login-session daemon with keychain access — OpenClaw's mechanism),
// then a config-supplied access token, then a login-session run, then a direct
// run. Callers wrap failures with an actionable hint.
func (g *gmailSource) gog(ctx context.Context, args ...string) (string, error) {
	if out, err, ok := g.bridge(ctx, args...); ok {
		return out, err
	}
	if tok, err := g.ensureToken(ctx); err != nil {
		return "", err
	} else if tok != "" {
		return execGogEnv(ctx, g.cfg.GogBin, []string{"GOG_ACCESS_TOKEN=" + tok}, args...)
	}
	if out, err := execGogAsUser(ctx, g.cfg.GogBin, args...); err == nil {
		return out, nil
	}
	return execGog(ctx, g.cfg.GogBin, args...)
}

// bridge forwards a gog invocation to the login-session daemon if one is
// reachable; the second return is true when the bridge handled the call.
func (g *gmailSource) bridge(ctx context.Context, args ...string) (string, error, bool) {
	if !g.bridgeChecked {
		g.bridgeChecked = true
		g.bridgeOK = gogd.Available(ctx, gogd.DefaultSocket())
	}
	if !g.bridgeOK {
		return "", nil, false
	}
	out, err := gogd.Call(ctx, gogd.DefaultSocket(), args...)
	return out, err, true
}

// UnsubscribeInfo reads the List-Unsubscribe header(s) from a Gmail thread's
// messages via gog.
func (g *gmailSource) UnsubscribeInfo(ctx context.Context, thread *model.Thread) (*model.UnsubscribeInfo, error) {
	if thread == nil {
		return unsub.ParseHeader("", ""), nil
	}
	out, err := g.gog(ctx, "gmail", "thread", "get", thread.ID,
		"-a", g.cfg.Account, "--results-only", "-j", "--no-input")
	if err != nil {
		return nil, err
	}
	var resp struct {
		Thread struct {
			Messages []struct {
				Payload struct {
					Headers []struct {
						Name  string `json:"name"`
						Value string `json:"value"`
					} `json:"headers"`
				} `json:"payload"`
			} `json:"messages"`
		} `json:"thread"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return nil, fmt.Errorf("parse gog thread get: %w", err)
	}
	for _, msg := range resp.Thread.Messages {
		var lu, lup string
		for _, h := range msg.Payload.Headers {
			switch strings.ToLower(h.Name) {
			case "list-unsubscribe":
				lu = h.Value
			case "list-unsubscribe-post":
				lup = h.Value
			}
		}
		if lu != "" || lup != "" {
			return unsub.ParseHeader(lu, lup), nil
		}
	}
	return unsub.ParseHeader("", ""), nil
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

// gmailHeader is a single message header.
type gmailHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// gmailMsgPayload recurses through a Gmail message's MIME payload.
type gmailMsgPayload struct {
	MimeType string          `json:"mimeType"`
	Body     struct {
		Data string `json:"data"`
	} `json:"body"`
	Headers []gmailHeader  `json:"headers"`
	Parts   []gmailMsgPayload `json:"parts"`
}

type gmailMsg struct {
	ID           string          `json:"id"`
	ThreadID     string          `json:"threadId"`
	InternalDate string          `json:"internalDate"`
	Payload      gmailMsgPayload `json:"payload"`
}

// ListMessages returns a thread's messages, oldest first, each with its
// plain-text body (walking MIME parts, decoding base64url and converting HTML).
func (g *gmailSource) ListMessages(ctx context.Context, thread *model.Thread) ([]*model.Message, error) {
	if thread == nil {
		return nil, nil
	}
	out, err := g.gog(ctx, "gmail", "thread", "get", thread.ID, "--full",
		"-a", g.cfg.Account, "--results-only", "-j", "--no-input")
	if err != nil {
		return nil, gogHint("gmail thread get", err, out)
	}
	var resp struct {
		Thread struct {
			Messages []gmailMsg `json:"messages"`
		} `json:"thread"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return nil, fmt.Errorf("parse gog thread get: %w", err)
	}
	msgs := make([]*model.Message, 0, len(resp.Thread.Messages))
	for _, m := range resp.Thread.Messages {
		fromName, fromEmail := splitFrom(headerValue(m.Payload.Headers, "From"))
		msg := &model.Message{
			ID:        m.ID,
			ThreadID:  thread.ID,
			Account:   model.AccountGmail,
			Subject:   headerValue(m.Payload.Headers, "Subject"),
			FromName:  fromName,
			FromEmail: strings.ToLower(fromEmail),
			Date:      parseUnixMillis(m.InternalDate),
			BodyText:  m.Payload.bodyText(),
		}
		if to := headerValue(m.Payload.Headers, "To"); to != "" {
			msg.To = splitToList(to)
		}
		msgs = append(msgs, msg)
	}
	sortMessagesOldest(msgs)
	return msgs, nil
}

func headerValue(hs []gmailHeader, name string) string {
	for _, h := range hs {
		if strings.EqualFold(h.Name, name) {
			return strings.TrimSpace(h.Value)
		}
	}
	return ""
}

// bodyText walks the MIME payload, preferring text/plain and converting the
// first HTML fallback, skipping attachments.
func (p gmailMsgPayload) bodyText() string {
	var sb strings.Builder
	p.appendText(&sb)
	return strings.TrimSpace(sb.String())
}

func (p gmailMsgPayload) appendText(sb *strings.Builder) {
	mt := strings.ToLower(p.MimeType)
	body := decodeBase64URL(p.Body.Data)
	switch {
	case mt == "text/plain":
		sb.WriteString(body)
		sb.WriteString("\n\n")
	case strings.HasPrefix(mt, "text/html"):
		sb.WriteString(htmlToText(body))
		sb.WriteString("\n\n")
	}
	for _, part := range p.Parts {
		part.appendText(sb)
	}
}

func parseUnixMillis(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n == 0 {
		return time.Time{}
	}
	return time.UnixMilli(n).UTC()
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
		case model.ActionSpam:
			args = append(args, "--add="+g.cfg.SpamLabel, "--remove=INBOX")
		case model.ActionDelete:
			args = append(args, "--add="+g.cfg.TrashLabel, "--remove=INBOX")
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

// gogHint wraps a gog failure with remediation when the cause looks like a
// credential/keychain access problem. sift first tries a login-session gog
// bridge; if that's unavailable the login keychain likely isn't reachable from
// this session, so we point at the bridge or a config-token fallback.
func gogHint(op string, err error, out string) error {
	base := classifyGogError(op, err, out)
	msg := strings.ToLower(out + " " + err.Error())
	switch {
	case strings.Contains(msg, "no auth") || strings.Contains(msg, "keychain") ||
		strings.Contains(msg, "credential") || strings.Contains(msg, "keyring") ||
		strings.Contains(msg, "interaction") || strings.Contains(msg, "not allowed"):
		return fmt.Errorf("%w — Gmail needs the login keychain, which isn't reachable from this session. Run `sift setup daemon` to install the login-session gog bridge (like OpenClaw), or add a credential to %s: [gmail] service_account_json, refresh_token (with client_id/client_secret), or access_token.", base, config.DefaultConfigPath())
	default:
		return base
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
