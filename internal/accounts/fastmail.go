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

// bodyPart and bodyValue mirror the JMAP Email body properties.
type bodyPart struct {
	PartID   string `json:"partId"`
	MimeType string `json:"mimeType"`
}
type bodyValue struct {
	Value    string `json:"value"`
	Encoding string `json:"encoding"`
}

// emailGetRow is the JMAP Email properties requested for message bodies.
type emailGetRow struct {
	ID         string `json:"id"`
	Subject    string `json:"subject"`
	Preview    string `json:"preview"`
	From       []struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"from"`
	To []struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"to"`
	ReceivedAt string      `json:"receivedAt"`
	TextBody   []bodyPart  `json:"textBody"`
	HtmlBody   []bodyPart  `json:"htmlBody"`
	BodyValues map[string]bodyValue `json:"bodyValues"`
}

// ListMessages returns a thread's messages, oldest first, each with its
// plain-text body (HTML converted where no text part exists).
func (s *fastmailSource) ListMessages(ctx context.Context, thread *model.Thread) ([]*model.Message, error) {
	if thread == nil {
		return nil, nil
	}
	// Resolve the thread's email ids. Thread/get is reliable; the Email/query
	// threadId filter is not.
	tg := jmap.NewCall("Thread/get", map[string]any{
		"accountId": s.cfg.AccountID,
		"ids":       []string{thread.ID},
	}, "t0")
	var env respEnvelope
	if err := s.cli.Call(&env, tg); err != nil {
		return nil, err
	}
	emailIDs := threadEmailIds(&env)
	if len(emailIDs) == 0 {
		return nil, nil
	}

	g := jmap.NewCall("Email/get", map[string]any{
		"accountId": s.cfg.AccountID,
		"ids":       emailIDs,
		"properties": []string{"subject", "from", "to", "receivedAt", "preview", "textBody", "htmlBody", "bodyValues"},
	}, "g0")
	var env2 respEnvelope
	if err := s.cli.Call(&env2, g); err != nil {
		return nil, err
	}

	var msgs []*model.Message
	for _, resp := range env2.MethodResponses {
		var name string
		_ = json.Unmarshal(resp[0], &name)
		if name != "Email/get" {
			continue
		}
		var args struct {
			List []emailGetRow `json:"list"`
		}
		if err := json.Unmarshal(resp[1], &args); err != nil {
			return nil, err
		}
		for _, e := range args.List {
			body := plaintextFromBody(e.TextBody, e.HtmlBody, e.BodyValues)
			if body == "" {
				body = strings.TrimSpace(e.Preview)
			}
			m := &model.Message{
				ID:       e.ID,
				ThreadID: thread.ID,
				Account:  model.AccountFastmail,
				Subject:  e.Subject,
				Date:     parseRFC3339(e.ReceivedAt),
				BodyText: body,
			}
			if len(e.From) > 0 {
				m.FromName = e.From[0].Name
				m.FromEmail = strings.ToLower(e.From[0].Email)
			}
			for _, a := range e.To {
				m.To = append(m.To, addr(a.Email, a.Name))
			}
			msgs = append(msgs, m)
		}
		break
	}
	sortMessagesOldest(msgs)
	return msgs, nil
}

// threadEmailIds extracts email ids from a Thread/get response.
func threadEmailIds(env *respEnvelope) []string {
	for _, resp := range env.MethodResponses {
		var name string
		_ = json.Unmarshal(resp[0], &name)
		if name != "Thread/get" {
			continue
		}
		var args struct {
			List []struct {
				EmailIDs []string `json:"emailIds"`
			} `json:"list"`
		}
		if err := json.Unmarshal(resp[1], &args); err != nil {
			return nil
		}
		for _, t := range args.List {
			if len(t.EmailIDs) > 0 {
				return t.EmailIDs
			}
		}
	}
	return nil
}

// plaintextFromBody assembles the plain text of an email from its text (or HTML)
// body parts, probing the JMAP bodyValues map.
func plaintextFromBody(text, html []bodyPart, bv map[string]bodyValue) string {
	parts := text
	isHTML := false
	if len(parts) == 0 {
		parts = html
		isHTML = true
	}
	var sb strings.Builder
	for _, p := range parts {
		v, ok := bv[p.PartID]
		if !ok {
			continue
		}
		val := decodeBody(v.Encoding, v.Value)
		if isHTML {
			val = htmlToText(val)
		}
		if v := strings.TrimSpace(val); v != "" {
			sb.WriteString(v)
			sb.WriteString("\n\n")
		}
	}
	return strings.TrimSpace(sb.String())
}

func sortMessagesOldest(msgs []*model.Message) {
	for i := 1; i < len(msgs); i++ {
		for j := i; j > 0 && msgs[j].Date.Before(msgs[j-1].Date); j-- {
			msgs[j], msgs[j-1] = msgs[j-1], msgs[j]
		}
	}
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
	case model.ActionSpam:
		target = s.cfg.Folders.Spam
	case model.ActionDelete:
		target = s.cfg.Folders.Trash
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

// threadEmailIDs resolves every email id belonging to a JMAP thread. It uses
// Thread/get (reliable) rather than the Email/query threadId filter, which
// Fastmail rejects/returns empty — previously this silently no-op'd Apply.
func (s *fastmailSource) threadEmailIDs(ctx context.Context, threadID string) ([]string, error) {
	tg := jmap.NewCall("Thread/get", map[string]any{
		"accountId": s.cfg.AccountID,
		"ids":       []string{threadID},
	}, "t0")
	var env respEnvelope
	if err := s.cli.Call(&env, tg); err != nil {
		return nil, err
	}
	if ids := threadEmailIds(&env); len(ids) > 0 {
		return ids, nil
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
