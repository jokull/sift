// Package ui implements the interactive triage TUI on top of bubbletea.
package ui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jokull/sift/internal/model"
	"github.com/jokull/sift/internal/state"
	"github.com/jokull/sift/internal/triage"
)

type appModel struct {
	engine *triage.Engine
	worker *triage.Worker
	store  *state.Store
	ctx    context.Context

	loaded     bool
	loadingMsg string

	candidates []*triage.Candidate
	autoJobs   []triage.AutoJob
	stats      triage.Stats
	warnings   []string

	cursor int

	// Drilldown navigation: candidates ▸ threads ▸ messages.
	level           int
	selThread       int
	selMsg          int
	threads         []*model.Thread
	messages        []*model.Message
	messagesLoading bool
	messagesErr     string
	msgScroll       int
	msgWidth        int
	msgLines        []string
	msgStarts       []int

	detail *detailModel

	progress map[string]triage.Progress // by label
	help     bool
	quitting bool
	width    int
	height   int
	now      time.Time
	frame    int // spinner frame while loading
}

type loadedMsg struct{ plan *triage.Plan }
type progressMsg struct{ u triage.Progress }
type tickMsg struct{}
type errMsg struct{ err error }
type bootProgressMsg struct{ text string }
type messagesMsg struct {
	threadID string
	msgs     []*model.Message
	err      error
}

// New builds the TUI state and binds the cancellation context.
func New(engine *triage.Engine, worker *triage.Worker, store *state.Store, ctx context.Context) *appModel {
	return &appModel{
		engine:   engine,
		worker:   worker,
		store:    store,
		ctx:      ctx,
		progress: map[string]triage.Progress{},
		now:      time.Now(),
	}
}

func (m *appModel) Init() tea.Cmd {
	m.loadingMsg = "Fetching inboxes…"
	m.frame = 0
	return tea.Batch(m.bootLoop(), m.loadPlan())
}

func (m *appModel) loadPlan() tea.Cmd {
	return func() tea.Msg {
		plan, err := m.engine.Load(m.ctx)
		if err != nil {
			return errMsg{err}
		}
		return loadedMsg{plan}
	}
}

func (m *appModel) waitProgress() tea.Cmd {
	return func() tea.Msg {
		select {
		case u := <-m.worker.Progress():
			return progressMsg{u}
		case <-time.After(2 * time.Second):
			return tickMsg{}
		}
	}
}

func (m *appModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.updateKey(msg)
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, m.idleCmd()
	case loadedMsg:
		m.loaded = true
		m.candidates = msg.plan.Candidates
		m.autoJobs = msg.plan.Auto
		m.stats = msg.plan.Stats
		m.warnings = msg.plan.Warnings
		m.loadingMsg = ""
		m.level = levelList
		m.threads = nil
		m.messages = nil
		m.messagesLoading = false
		m.messagesErr = ""
		m.msgScroll = 0
		m.submitAutoJobs()
		return m, m.waitProgress()
	case errMsg:
		m.loadingMsg = ""
		// Surface the error in the main view instead of spinning forever.
		m.loaded = true
		m.warnings = []string{"ERROR: " + msg.err.Error()}
		return m, m.waitProgress()
	case progressMsg:
		u := msg.u
		m.progress[u.Label] = u
		return m, m.waitProgress()
	case bootProgressMsg:
		if m.loaded {
			return m, m.waitProgress()
		}
		m.frame++
		m.loadingMsg = msg.text
		return m, m.bootLoop()
	case messagesMsg:
		return m, m.onMessages(msg)
	case tea.MouseMsg:
		return m.updateMouse(msg)
	case tickMsg:
		if !m.loaded {
			m.frame++
			return m, m.bootLoop()
		}
		return m, m.waitProgress()
	}
	return m, m.idleCmd()
}

// idleCmd keeps the right loop running: while booting, drive the spinner and
// boot progress; once loaded, poll the worker HUD.
func (m *appModel) idleCmd() tea.Cmd {
	if !m.loaded {
		return m.bootLoop()
	}
	return m.waitProgress()
}

// bootLoop animates the loading screen. It returns the next boot progress stage
// when one is available, otherwise a spinner tick.
func (m *appModel) bootLoop() tea.Cmd {
	return func() tea.Msg {
		select {
		case s := <-m.engine.Progress():
			return bootProgressMsg{text: s}
		case <-time.After(spinnerInterval):
			return tickMsg{}
		}
	}
}

func (m *appModel) submitAutoJobs() {
	// Group auto jobs by (account, action) into single worker jobs for the HUD.
	byKey := map[string]*triage.Job{}
	order := []string{}
	for _, j := range m.autoJobs {
		key := string(j.Account) + "|" + string(j.Action)
		job, ok := byKey[key]
		if !ok {
			job = &triage.Job{
				Account: j.Account,
				Action:  j.Action,
				Label:   fmt.Sprintf("%s → %s", j.Account, actionName(j.Action)),
			}
			byKey[key] = job
			order = append(order, key)
		}
		job.Threads = append(job.Threads, j.Thread)
	}
	for _, k := range order {
		m.worker.Submit(byKey[k])
	}
}

func (m *appModel) submitAutoActions() {}

func (m *appModel) applyDecision(idx int, action model.Action, wholeCohort bool) {
	if idx < 0 || idx >= len(m.candidates) {
		return
	}
	can := m.candidates[idx]

	// Record per-sender decisions so future runs remember them.
	if can.Pred.Category == model.CategoryPromotion || can.Pred.Category == model.CategoryTransactional {
		if m.store != nil {
			switch action {
			case model.ActionKeep:
				_ = m.store.AddWhitelist(can.Thread.SenderKey())
				_ = m.store.SaveSenderDecision(can.Thread.SenderKey(), model.ActionKeep)
			case model.ActionUnsubscribe:
				_ = m.store.AddUnsubscribed(can.Thread.SenderKey())
				_ = m.store.SaveSenderDecision(can.Thread.SenderKey(), model.ActionUnsubscribe)
			default:
				_ = m.store.SaveSenderDecision(can.Thread.SenderKey(), action)
			}
		}
	}

	// Determine which candidates this decision covers: the single row, or — for a
	// whole-cohort/hulk action — every row sharing the same sender-group+category.
	var handled []*triage.Candidate
	if wholeCohort {
		// A bulk decision acts on every triage candidate from this sender,
		// across categories (auto actions and keeps are handled separately).
		group := can.Thread.SenderKey()
		for _, c := range m.candidates {
			if c.Thread.SenderKey() == group {
				handled = append(handled, c)
			}
		}
	} else {
		handled = []*triage.Candidate{can}
	}

	// Submit work per account (a cohort can span both mailboxes).
	if action != model.ActionKeep {
		byAccount := map[model.Account][]*model.Thread{}
		for _, c := range handled {
			byAccount[c.Thread.Account] = append(byAccount[c.Thread.Account], c.Thread)
		}
		for acct, ts := range byAccount {
			if m.worker != nil {
				m.worker.Submit(&triage.Job{
					Account: acct,
					Threads: ts,
					Action:  action,
					Label:   fmt.Sprintf("%s x%d", actionName(action), len(ts)),
				})
			}
		}
	}

	// Remove handled candidates optimistically (before the worker finishes), so
	// every row in the cohort vanishes from the list immediately.
	handledIDs := map[string]bool{}
	for _, c := range handled {
		handledIDs[c.Thread.ID] = true
	}
	kept := m.candidates[:0]
	for _, c := range m.candidates {
		if !handledIDs[c.Thread.ID] {
			kept = append(kept, c)
		}
	}
	m.candidates = kept

	if m.cursor >= len(m.candidates) {
		m.cursor = len(m.candidates) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	m.detail = nil
}

func actionName(a model.Action) string {
	switch a {
	case model.ActionKeep:
		return "keep"
	case model.ActionArchive:
		return "archive"
	case model.ActionUnsubscribe:
		return "unsubscribe"
	case model.ActionReceipts:
		return "receipts"
	case model.ActionReading:
		return "reading"
	case model.ActionSpam:
		return "spam"
	case model.ActionDelete:
		return "delete"
	}
	return string(a)
}
