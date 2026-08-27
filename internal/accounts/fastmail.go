package accounts

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jokull/sift/internal/config"
	"github.com/jokull/sift/internal/jmap"
	"github.com/jokull/sift/internal/model"
	"github.com/jokull/sift/internal/unsub"
)

type fastmailSource struct {
	cfg  *config.FastmailConfig
	cli  *jmap.Client
	token string
}

func newFastmail(cfg *config.FastmailConfig) (*fastmailSource, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil fastmail config")
	}
	return &fastmailSource{
		cfg:   cfg,
		token: cfg.Token,
		cli:   jmap.NewClient(cfg.Token, cfg.APIURL, cfg.AccountID),
	}, nil
}

func (s *fastmailSource) Account() model.Account { return model.AccountFastmail }

// respEnvelope is the JMAP `methodResponses` envelope.
type respEnvelope struct {
	MethodResponses [][]json.RawMessage `json:"methodResponses"`
}

// emailRow is the subset of JMAP Email properties we request.
type emailRow struct {
	ID         string `json:"id"`
	ThreadID   string `json:"threadId"`
	Subject    string `json:"subject"`
	From       []struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"from"`
	ReceivedAt string          `json:"receivedAt"`
	Preview    string          `json:"preview"`
	Keywords   map[string]bool `json:"keywords"`
	MailboxIDs map[string]bool `json:"mailboxIds"`
}

func (s *fastmailSource) ListThreads(ctx context.Context, limit int) ([]*model.Thread, error) {
	if limit <= 0 {
		limit = 60
	}
	q := jmap.NewCall("Email/query", map[string]any{
		"accountId": s.cfg.AccountID,
		"filter":    map[string]any{"inMailbox": s.cfg.Folders.Inbox},
		"sort":      []map[string]any{{"property": "receivedAt", "isAscending": false}},
		"limit":     limit,
	}, "q0")

	var env respEnvelope
	if err := s.cli.Call(&env, q); err != nil {
		return nil, err
	}
	ids, err := queryIDs(&env)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}

	g := jmap.NewCall("Email/get", map[string]any{
		"accountId": s.cfg.AccountID,
		"ids":       ids,
		"properties": []string{"subject", "from", "receivedAt", "threadId", "mailboxIds", "preview", "keywords"},
	}, "g0")
	if err := s.cli.Call(&env, g); err != nil {
		return nil, err
	}

	rows := []emailRow{}
	for _, resp := range env.MethodResponses {
		var name string
		_ = json.Unmarshal(resp[0], &name)
		if name != "Email/get" {
			continue
		}
		var args struct {
			List []emailRow `json:"list"`
		}
		if err := json.Unmarshal(resp[1], &args); err != nil {
			return nil, err
		}
		rows = args.List
		break
	}

	// Group by JMAP threadId; each thread keeps the newest message's metadata.
	byThread := map[string]*model.Thread{}
	for _, r := range rows {
		t, ok := byThread[r.ThreadID]
		if !ok {
			t = &model.Thread{
				ID:           r.ThreadID,
				Account:      model.AccountFastmail,
				Subject:      r.Subject,
				Date:         parseRFC3339(r.ReceivedAt),
				Snippet:      r.Preview,
			}
			if len(r.From) > 0 {
				t.FromName = r.From[0].Name
				t.FromEmail = strings.ToLower(r.From[0].Email)
			}
			byThread[r.ThreadID] = t
		}
		t.MessageCount++
		if !isSeen(r.Keywords) {
			t.Unread++
		}
		if r.Subject != "" && t.Subject == "" {
			t.Subject = r.Subject
		}
		// newest wins
		if d := parseRFC3339(r.ReceivedAt); d.After(t.Date) {
			t.Date = d
			t.Subject = r.Subject
			if len(r.From) > 0 {
				t.FromName = r.From[0].Name
				t.FromEmail = strings.ToLower(r.From[0].Email)
			}
			t.Snippet = r.Preview
		}
	}

	threads := make([]*model.Thread, 0, len(byThread))
	for _, t := range byThread {
		threads = append(threads, t)
	}
	sortThreadsNewest(threads)
	return threads, nil
}

// Apply moves whole threads to a destination folder (or archives them).
func (s *fastmailSource) Apply(ctx context.Context, threads []*model.Thread, action model.Action) error {
	if len(threads) == 0 {
		return nil
	}
	var target string
	switch action {
	case model.ActionReceipts:
		target = s.cfg.Folders.Receipts
	case model.ActionReading:
		target = s.cfg.Folders.Reading
	case model.ActionArchive, model.ActionUnsubscribe:
		target = s.cfg.Folders.Archive
	default:
		return fmt.Errorf("fastmail: unsupported action %q", action)
	}
	if target == "" {
		return fmt.Errorf("fastmail: target folder not configured for action %q", action)
	}

	for _, th := range threads {
		emailIDs, err := s.threadEmailIDs(ctx, th.ID)
		if err != nil {
			return fmt.Errorf("thread %s: %w", th.ID, err)
		}
		if len(emailIDs) == 0 {
			continue
		}
		update := map[string]any{}
		for _, id := range emailIDs {
			update[id] = map[string]any{"mailboxIds": map[string]bool{target: true}}
		}
		set := jmap.NewCall("Email/set", map[string]any{
			"accountId": s.cfg.AccountID,
			"update":    update,
		}, fmt.Sprintf("set-%s", th.ID))
		var env respEnvelope
		if err := s.cli.Call(&env, set); err != nil {
			return fmt.Errorf("thread %s: %w", th.ID, err)
		}
		// Surface notSaved if present.
		for _, resp := range env.MethodResponses {
			var name string
			_ = json.Unmarshal(resp[0], &name)
			if name == "Email/set" {
				var args struct {
					NotSaved map[string]any `json:"notSaved"`
				}
				if err := json.Unmarshal(resp[1], &args); err == nil && len(args.NotSaved) > 0 {
					return fmt.Errorf("thread %s: %d email(s) not saved", th.ID, len(args.NotSaved))
				}
			}
		}
	}
	return nil
}

// threadEmailIDs resolves every email id belonging to a JMAP thread.
func (s *fastmailSource) threadEmailIDs(ctx context.Context, threadID string) ([]string, error) {
	q := jmap.NewCall("Email/query", map[string]any{
		"accountId": s.cfg.AccountID,
		"filter":    map[string]any{"threadId": threadID},
	}, "qq")
	var env respEnvelope
	if err := s.cli.Call(&env, q); err != nil {
		return nil, err
	}
	for _, resp := range env.MethodResponses {
		var name string
		_ = json.Unmarshal(resp[0], &name)
		if name == "Email/query" {
			var args struct {
				IDs []string `json:"ids"`
			}
			if err := json.Unmarshal(resp[1], &args); err != nil {
				return nil, err
			}
			return args.IDs, nil
		}
	}
	return nil, nil
}

// UnsubscribeInfo reads the List-Unsubscribe header(s) from a JMAP thread.
func (s *fastmailSource) UnsubscribeInfo(ctx context.Context, thread *model.Thread) (*model.UnsubscribeInfo, error) {
	if thread == nil {
		return unsub.ParseHeader("", ""), nil
	}
	ids, err := s.threadEmailIDs(ctx, thread.ID)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return unsub.ParseHeader("", ""), nil
	}
	g := jmap.NewCall("Email/get", map[string]any{
		"accountId": s.cfg.AccountID,
		"ids":       []string{ids[0]},
		"properties": []string{"header:List-Unsubscribe", "header:List-Unsubscribe-Post"},
	}, "g0")
	var env respEnvelope
	if err := s.cli.Call(&env, g); err != nil {
		return nil, err
	}
	for _, resp := range env.MethodResponses {
		var name string
		_ = json.Unmarshal(resp[0], &name)
		if name != "Email/get" {
			continue
		}
		var args struct {
			List []struct {
				LU  string `json:"header:List-Unsubscribe"`
				LUP string `json:"header:List-Unsubscribe-Post"`
			} `json:"list"`
		}
		if err := json.Unmarshal(resp[1], &args); err != nil {
			return nil, err
		}
		if len(args.List) > 0 {
			return unsub.ParseHeader(args.List[0].LU, args.List[0].LUP), nil
		}
	}
	return unsub.ParseHeader("", ""), nil
}

// EnsureFolders verifies the Receipts and Reading mailboxes exist. Fastmail
// already has them configured; this is a no-op safety check.
func (s *fastmailSource) EnsureFolders(ctx context.Context) error {
	boxes, err := s.cli.Mailboxes()
	if err != nil {
		return err
	}
	for _, b := range boxes {
		switch b.Name {
		case "Receipts":
			if s.cfg.Folders.Receipts == "" {
				s.cfg.Folders.Receipts = b.ID
			}
		case "Reading":
			if s.cfg.Folders.Reading == "" {
				s.cfg.Folders.Reading = b.ID
			}
		}
	}
	return nil
}

func parseRFC3339(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t.UTC()
}

// isSeen reports whether the email's keywords mark it as read ($seen present).
func isSeen(kw map[string]bool) bool {
	seen, ok := kw["$seen"]
	if !ok {
		return false
	}
	return seen
}

// queryIDs extracts the ids list from a JMAP *get/query response envelope.
func queryIDs(env *respEnvelope) ([]string, error) {
	for _, resp := range env.MethodResponses {
		var name string
		_ = json.Unmarshal(resp[0], &name)
		if name != "Email/query" && name != "Email/get" {
			continue
		}
		var args struct {
			IDs        []string `json:"ids"`
			List       []struct{ ID string `json:"id"` } `json:"list"`
			NotFound   []string `json:"notFound"`
		}
		if err := json.Unmarshal(resp[1], &args); err != nil {
			return nil, err
		}
		if len(args.IDs) > 0 {
			return args.IDs, nil
		}
		if len(args.List) > 0 {
			ids := make([]string, 0, len(args.List))
			for _, e := range args.List {
				ids = append(ids, e.ID)
			}
			return ids, nil
		}
		if len(args.NotFound) > 0 {
			return nil, nil
		}
	}
	return nil, nil
}

func sortThreadsNewest(ts []*model.Thread) {
	// insertion-ish order already newest-first from the query, but re-sort to
	// be safe for merged sets.
	for i := 1; i < len(ts); i++ {
		for j := i; j > 0 && ts[j].Date.After(ts[j-1].Date); j-- {
			ts[j], ts[j-1] = ts[j-1], ts[j]
		}
	}
}
