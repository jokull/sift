package ui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/jokull/sift/internal/model"
)

func (m *appModel) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" || msg.String() == "q" && m.detail == nil {
		return m.quit()
	}
	if msg.String() == "help" || msg.String() == "?" {
		m.status = "navigate ↑/↓ or j/k · ⏎ detail · a archive · u unsubscribe · r receipts · n reading · s keep · shift = whole sender · b back · q quit"
		return m, m.waitProgress()
	}

	// Detail window captures keys.
	if m.detail != nil {
		return m.detailKey(msg)
	}

	switch msg.String() {
	case "q":
		return m.quit()
	case "j", "down":
		m.move(1)
	case "k", "up":
		m.move(-1)
	case "g":
		m.cursor = 0
	case "G":
		m.cursor = len(m.candidates) - 1
	case "enter", " ", "l":
		if m.detailOpenEnabled() {
			m.openDetail()
		}
	case "a":
		m.applyDecision(m.cursor, model.ActionArchive, false)
	case "u":
		m.applyDecision(m.cursor, model.ActionUnsubscribe, false)
	case "r":
		m.applyDecision(m.cursor, model.ActionReceipts, false)
	case "n":
		m.applyDecision(m.cursor, model.ActionReading, false)
	case "A":
		m.applyDecision(m.cursor, model.ActionArchive, true)
	case "U":
		m.applyDecision(m.cursor, model.ActionUnsubscribe, true)
	case "R":
		m.applyDecision(m.cursor, model.ActionReceipts, true)
	case "N":
		m.applyDecision(m.cursor, model.ActionReading, true)
	case "s":
		// keep: just remove from queue, no worker action.
		m.handleKeep(m.cursor)
	}
	return m, m.waitProgress()
}

func (m *appModel) detailKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "b", "esc":
		m.detail = nil
	case "a":
		m.applyDecision(m.cursor, model.ActionArchive, false)
	case "u":
		m.applyDecision(m.cursor, model.ActionUnsubscribe, false)
	case "r":
		m.applyDecision(m.cursor, model.ActionReceipts, false)
	case "n":
		m.applyDecision(m.cursor, model.ActionReading, false)
	case "s":
		m.handleKeep(m.cursor)
	case "x", "enter":
		m.applyDecision(m.cursor, m.detail.defaultAction(), true)
	}
	return m, m.waitProgress()
}

func (m *appModel) move(delta int) {
	if len(m.candidates) == 0 {
		m.cursor = 0
		return
	}
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.candidates) {
		m.cursor = len(m.candidates) - 1
	}
}

func (m *appModel) detailOpenEnabled() bool { return len(m.candidates) > 0 }

func (m *appModel) openDetail() {
	if m.cursor >= 0 && m.cursor < len(m.candidates) {
		m.detail = newDetail(m.candidates[m.cursor])
	}
}

func (m *appModel) handleKeep(idx int) {
	if idx < 0 || idx >= len(m.candidates) {
		return
	}
	var c *model.Candidate
	_ = c
	can := m.candidates[idx]
	if m.store != nil && (can.Pred.Category == model.CategoryPromotion || can.Pred.Category == model.CategoryTransactional) {
		_ = m.store.AddWhitelist(can.Thread.SenderKey())
		_ = m.store.SaveSenderDecision(can.Thread.SenderKey(), model.ActionKeep)
	}
	m.candidates = append(m.candidates[:idx], m.candidates[idx+1:]...)
	if m.cursor >= len(m.candidates) {
		m.cursor = len(m.candidates) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	m.detail = nil
}

func (m *appModel) quit() (tea.Model, tea.Cmd) {
	m.quitting = true
	m.ctx = nil
	return m, tea.Quit
}
