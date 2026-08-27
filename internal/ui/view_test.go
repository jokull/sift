package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jokull/sift/internal/model"
	"github.com/jokull/sift/internal/triage"
)

func TestRenderListAndDetail(t *testing.T) {
	now := time.Now()
	m := &appModel{
		loaded: true,
		now:    now,
		candidates: []*triage.Candidate{
			{Thread: &model.Thread{ID: "1", Account: model.AccountGmail, Subject: "Your invoice", FromEmail: "billing@x.com", FromName: "Billing", Date: now.Add(-2 * time.Hour), MessageCount: 1}, Pred: model.Prediction{Category: model.CategoryReceipt, Action: model.ActionReceipts, Confidence: 0.9}},
			{Thread: &model.Thread{ID: "2", Account: model.AccountFastmail, Subject: "New letter from friend", FromEmail: "friend@y.com", Date: now.Add(-5 * time.Hour)}, Pred: model.Prediction{Category: model.CategoryKeep, Action: model.ActionKeep, Confidence: 0.8}},
		},
		today:   []*model.Thread{{ID: "3", Account: model.AccountFastmail, Date: now}},
		progress: map[string]triage.Progress{
			"gmail → archive": {Label: "gmail → archive", Account: model.AccountGmail, Total: 5, Done: 2, Active: true},
			"fastmail → receipts": {Label: "fastmail → receipts", Account: model.AccountFastmail, Total: 15, Done: 15},
		},
	}
	out := m.View()
	if out == "" {
		t.Fatal("empty view")
	}
	if !strings.Contains(out, "sift") || !strings.Contains(out, "Your invoice") {
		t.Fatalf("view missing content:\n%s", out)
	}
	if !strings.Contains(out, "in progress") || !strings.Contains(out, "confirmed") {
		t.Fatalf("task footer missing state counters:\n%s", out)
	}
	if !strings.Contains(out, "1 in progress · 1 confirmed") {
		t.Fatalf("task footer missing aggregate counter:\n%s", out)
	}

	// Detail overlay.
	m.detail = newDetail(m.candidates[0])
	out2 := m.View()
	if !strings.Contains(out2, "decision") || !strings.Contains(out2, "from:") {
		t.Fatalf("detail view missing content:\n%s", out2)
	}
	_ = context.Background
}
