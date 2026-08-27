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
	today      []*model.Thread
	stats      triage.Stats
	warnings   []string

	cursor int

	detail *detailModel

	progress map[string]triage.Progress // by label
	help     bool
	quitting bool
	width    int
	height   int
	now      time.Time
}

type loadedMsg struct{ plan *triage.Plan }
type progressMsg struct{ u triage.Progress }
type tickMsg struct{}
type errMsg struct{ err error }

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
	return tea.Batch(m.waitProgress(), m.loadPlan())
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
		return m, m.waitProgress()
	case loadedMsg:
		m.loaded = true
		m.candidates = msg.plan.Candidates
		m.autoJobs = msg.plan.Auto
		m.today = msg.plan.Today
		m.stats = msg.plan.Stats
		m.warnings = msg.plan.Warnings
		m.loadingMsg = ""
		m.submitAutoJobs()
		return m, m.waitProgress()
	case errMsg:
		m.loadingMsg = ""
		m.warnings = []string{"ERROR: " + msg.err.Error()}
		return m, m.waitProgress()
	case progressMsg:
		u := msg.u
		m.progress[u.Label] = u
		return m, m.waitProgress()
	case tickMsg:
		return m, m.waitProgress()
	}
	return m, m.waitProgress()
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
	threads := []*model.Thread{can.Thread}
	if wholeCohort && len(can.Cohort) > 0 {
		threads = append(threads, can.Cohort...)
	}

	// Record per-sender decisions so future runs remember them.
	if can.Pred.Category == model.CategoryPromotion || can.Pred.Category == model.CategoryTransactional {
		if m.store != nil {
			switch action {
			case model.ActionKeep:
				_ = m.store.AddWhitelist(can.Thread.SenderGroup())
				_ = m.store.SaveSenderDecision(can.Thread.SenderGroup(), model.ActionKeep)
			case model.ActionUnsubscribe:
				_ = m.store.AddUnsubscribed(can.Thread.SenderGroup())
				_ = m.store.SaveSenderDecision(can.Thread.SenderGroup(), model.ActionUnsubscribe)
			default:
				_ = m.store.SaveSenderDecision(can.Thread.SenderGroup(), action)
			}
		}
	}

	if action != model.ActionKeep {
		label := fmt.Sprintf("%s x%d", actionName(action), len(threads))
		if m.worker != nil {
			m.worker.Submit(&triage.Job{
				Account: can.Thread.Account,
				Threads: threads,
				Action:  action,
				Label:   label,
			})
		}
	}

	// Remove handled candidates. wholeCohort removes every cohort member.
	if wholeCohort {
		remove := map[string]bool{}
		remove[can.Thread.ID] = true
		for _, t := range can.Cohort {
			remove[t.ID] = true
		}
		kept := m.candidates[:0]
		for _, c := range m.candidates {
			if !remove[c.Thread.ID] {
				kept = append(kept, c)
			}
		}
		m.candidates = kept
	} else {
		m.candidates = append(m.candidates[:idx], m.candidates[idx+1:]...)
	}
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
	}
	return string(a)
}

func planStatus(plan *triage.Plan) string {
	return fmt.Sprintf("%d to review · %d auto-pluck (receipts) · %d auto-read · %d today untouched",
		len(plan.Candidates), plan.Stats.AutoReceipts, plan.Stats.AutoReading, len(plan.Today))
}
