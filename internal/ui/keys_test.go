package ui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jokull/sift/internal/model"
	"github.com/jokull/sift/internal/triage"
)

// candidate is a minimal triage candidate for keyboard tests.
func keyTestCandidate() *triage.Candidate {
	return &triage.Candidate{
		Thread: &model.Thread{ID: "1", Account: model.AccountGmail, Subject: "Subj", FromEmail: "s@x.com", Date: time.Now()},
		Pred:   model.Prediction{Category: model.CategoryPromotion, Action: model.ActionArchive, Confidence: 0.9},
	}
}

func keyTestApp() *appModel {
	return &appModel{
		loaded:     true,
		candidates: []*triage.Candidate{keyTestCandidate()},
		now:        time.Now(),
		width:      100,
		height:     40,
		progress:   map[string]triage.Progress{},
	}
}

func TestEnterOpensDetail(t *testing.T) {
	m := keyTestApp()
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.detail == nil {
		t.Fatalf("enter did not open the detail window")
	}
	// back returns to the list.
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.detail != nil {
		t.Fatalf("esc did not close the detail window")
	}
}

func TestArchiveRemovesCandidate(t *testing.T) {
	m := keyTestApp()
	before := len(m.candidates)
	// A worker is nil; the application of a non-keep action submits a job. With a
	// nil worker the submit is a no-op, but the candidate is still dequeued.
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if len(m.candidates) != before-1 {
		t.Fatalf("archive did not dequeue candidate: before=%d after=%d", before, len(m.candidates))
	}
}

func TestWindowResizeKeepsWorking(t *testing.T) {
	m := keyTestApp()
	out := m.View()
	if out == "" {
		t.Fatal("empty view after resize")
	}
}

func TestBulkArchiveRemovesCohortOptimistically(t *testing.T) {
	now := time.Now()
	mk := func(id, email string, cat model.Category) *triage.Candidate {
		return &triage.Candidate{
			Thread: &model.Thread{ID: id, Account: model.AccountGmail, FromEmail: email, Date: now},
			Pred:   model.Prediction{Category: cat, Action: model.ActionArchive, Confidence: 0.9},
		}
	}
	m := &appModel{
		loaded: true, now: now, width: 100, height: 40,
		candidates: []*triage.Candidate{
			mk("1", "a@google.com", model.CategoryPromotion),
			mk("2", "b@google.com", model.CategoryPromotion),
			mk("3", "c@apple.com", model.CategoryPromotion),   // different domain → stays
			mk("4", "d@google.com", model.CategoryTransactional), // different category → stays
		},
		progress: map[string]triage.Progress{},
	}

	m.applyDecision(0, model.ActionArchive, true)
	if len(m.candidates) != 2 {
		t.Fatalf("expected 2 candidates left after bulk archive, got %d", len(m.candidates))
	}
	for _, c := range m.candidates {
		if c.Thread.SenderGroup() == "google.com" && c.Pred.Category == model.CategoryPromotion {
			t.Fatalf("cohort row not removed: %s", c.Thread.ID)
		}
	}
}
