package triage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jokull/sift/internal/accounts"
	"github.com/jokull/sift/internal/model"
	"github.com/jokull/sift/internal/state"
)

// fakeSource is a minimal in-memory Source for exercising the engine without
// live accounts or an AI client.
type fakeSource struct {
	account model.Account
	threads []*model.Thread
}

func (f *fakeSource) Account() model.Account { return f.account }
func (f *fakeSource) ListThreads(_ context.Context, _ int) ([]*model.Thread, error) {
	return f.threads, nil
}
func (f *fakeSource) ListThreadsBySender(_ context.Context, sender string, limit int) ([]*model.Thread, bool, error) {
	var out []*model.Thread
	for _, t := range f.threads {
		if t.SenderKey() == sender {
			out = append(out, t)
		}
	}
	if len(out) > limit {
		return out[:limit], true, nil
	}
	return out, false, nil
}
func (f *fakeSource) Apply(context.Context, []*model.Thread, model.Action) error { return nil }
func (f *fakeSource) EnsureFolders(context.Context) error                        { return nil }
func (f *fakeSource) UnsubscribeInfo(context.Context, *model.Thread) (*model.UnsubscribeInfo, error) {
	return nil, nil
}
func (f *fakeSource) ListMessages(context.Context, *model.Thread) ([]*model.Message, error) {
	return nil, nil
}

// TestLoadProcessesTodayMail guards the removal of the "ignore the first 24
// hours" rule: a receipt that arrived today must be auto-plucked, not left
// untouched.
func TestLoadProcessesTodayMail(t *testing.T) {
	now := time.Now()
	src := &fakeSource{account: model.AccountFastmail, threads: []*model.Thread{
		{ID: "1", Account: model.AccountFastmail, Subject: "Your receipt", FromEmail: "billing@x.com", Date: now},
	}}
	e := New(map[model.Account]accounts.Source{model.AccountFastmail: src}, nil, nil)
	plan, err := e.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(plan.Auto) != 1 || plan.Auto[0].Action != model.ActionReceipts {
		t.Fatalf("today's receipt not auto-plucked: Auto=%+v", plan.Auto)
	}
	if plan.Stats.Loaded != 1 || plan.Stats.Candidates != 0 {
		t.Fatalf("unexpected stats: %+v", plan.Stats)
	}
}

// TestLoadEmitsBootProgress guards boot progress reporting: the engine surfaces
// staged status on Progress() for the TUI loading screen.
func TestLoadEmitsBootProgress(t *testing.T) {
	src := &fakeSource{account: model.AccountFastmail, threads: []*model.Thread{
		{ID: "1", Account: model.AccountFastmail, Subject: "Hi", FromEmail: "a@b.com", Date: time.Now()},
	}}
	e := New(map[model.Account]accounts.Source{model.AccountFastmail: src}, nil, nil)
	if _, err := e.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	select {
	case s := <-e.Progress():
		if s == "" {
			t.Fatalf("empty boot progress message")
		}
	default:
		t.Fatalf("expected a boot progress message")
	}
}

// TestCohortThreads guards the server-true cohort count: it fetches the
// sender's full inbox thread set (beyond the loaded window) and returns only
// triage-category threads, using cached classifications.
func TestCohortThreads(t *testing.T) {
	ctx := context.Background()
	sender := "no-reply@esp.com"
	threads := []*model.Thread{
		{ID: "1", Account: model.AccountFastmail, FromEmail: sender, Subject: "a promo"},
		{ID: "2", Account: model.AccountFastmail, FromEmail: sender, Subject: "an invoice"},
		{ID: "3", Account: model.AccountFastmail, FromEmail: sender, Subject: "a newsletter"},
		{ID: "4", Account: model.AccountFastmail, FromEmail: sender, Subject: "a friend"},
	}
	src := &fakeSource{account: model.AccountFastmail, threads: threads}

	st, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	for _, id := range []string{"1", "2", "3", "4"} {
		if err := st.SaveClassification(string(model.AccountFastmail), id, model.Prediction{
			Category: map[string]model.Category{"1": model.CategoryPromotion, "2": model.CategoryReceipt, "3": model.CategoryNewsletter, "4": model.CategoryKeep}[id],
			Action:   model.ActionArchive,
		}); err != nil {
			t.Fatalf("save classification: %v", err)
		}
	}

	e := New(map[model.Account]accounts.Source{model.AccountFastmail: src}, nil, st)
	got, truncated, err := e.CohortThreads(ctx, model.AccountFastmail, sender)
	if err != nil {
		t.Fatalf("CohortThreads: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 triage thread in deep cohort, got %d", len(got))
	}
	for _, th := range got {
		if th.ID != "1" {
			t.Fatalf("expected thread 1 (promotion), got %s", th.ID)
		}
	}
	if truncated {
		t.Fatalf("unexpected truncation with 4 threads")
	}
}
