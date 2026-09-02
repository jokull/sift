package triage

import (
	"context"
	"testing"
	"time"

	"github.com/jokull/sift/internal/accounts"
	"github.com/jokull/sift/internal/model"
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
