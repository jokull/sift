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
			mk("1", "no-reply@esp.com", model.CategoryPromotion),     // in cohort (same sender)
			mk("2", "no-reply@esp.com", model.CategoryPromotion),     // in cohort
			mk("3", "other@esp.com", model.CategoryPromotion),        // different sender → stays
			mk("4", "no-reply@esp.com", model.CategoryTransactional), // same sender, diff category → also removed
		},
		progress: map[string]triage.Progress{},
	}

	m.applyDecision(0, model.ActionArchive, true)
	// Sender-wide bulk: every candidate from "no-reply@esp.com" is removed,
	// regardless of category; only the different-sender row stays.
	if len(m.candidates) != 1 {
		t.Fatalf("expected 1 candidate left after sender-wide bulk archive, got %d: %+v", len(m.candidates), m.candidates)
	}
	for _, c := range m.candidates {
		if c.Thread.FromEmail == "no-reply@esp.com" {
			t.Fatalf("sender cohort row not removed: %s/%s", c.Thread.ID, c.Pred.Category)
		}
	}
}
