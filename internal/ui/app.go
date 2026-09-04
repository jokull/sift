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

	// candidates is the currently displayed decision list — a view onto
	// allCandidates filtered by showUnread. The list is what rendering,
	// navigation and decisions operate on.
	candidates []*triage.Candidate
	// allCandidates is the full working decision list (source of truth); a
	// decision removes threads here, and candidates is rebuilt from it.
	allCandidates []*triage.Candidate
	// showUnread filters candidates down to threads with unread messages.
	showUnread bool
	autoJobs   []triage.AutoJob
	stats      triage.Stats
	warnings   []string

	// Deep cohort counts, keyed "account|senderKey", fetched asynchronously from
	// the server so ×N reflects the true mailbox (not just the loaded window).
	// deepCohortTrunc marks senders whose volume exceeded the fetch cap; the
	// count is then approximate. deepCohortPending dedupes in-flight fetches.
	deepCohorts       map[cohortKey]int
	deepCohortTrunc   map[cohortKey]bool
	deepCohortPending map[cohortKey]bool
	deepCohortSem     chan struct{}                 // bounds concurrent fetches (3)
	deepCohortThreads map[cohortKey][]*model.Thread // server-true triage threads per sender

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

// cohortCountMsg carries an async server-true cohort count for one sender.
type cohortCountMsg struct {
	key       cohortKey
	threads   []*model.Thread
	count     int
	truncated bool
	err       error
}

// cohortKey identifies a sender within an account for the deep cohort count.
type cohortKey struct {
	account model.Account
	sender  string
}

// New builds the TUI state and binds the cancellation context.
func New(engine *triage.Engine, worker *triage.Worker, store *state.Store, ctx context.Context) *appModel {
	return &appModel{
		engine:            engine,
		worker:            worker,
		store:             store,
		ctx:               ctx,
		progress:          map[string]triage.Progress{},
		deepCohorts:       map[cohortKey]int{},
		deepCohortTrunc:   map[cohortKey]bool{},
		deepCohortPending: map[cohortKey]bool{},
		deepCohortSem:     make(chan struct{}, deepCohortConcurrency),
		deepCohortThreads: map[cohortKey][]*model.Thread{},
		now:               time.Now(),
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
		m.allCandidates = msg.plan.Candidates
		m.autoJobs = msg.plan.Auto
		m.stats = msg.plan.Stats
		m.warnings = msg.plan.Warnings
		m.loadingMsg = ""
		m.level = levelList
		m.threads = nil
		m.messages = nil
		m.refreshCandidates()
		m.submitAutoJobs()
		return m, tea.Batch(m.waitProgress(), m.fetchDeepCohorts())
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
	case cohortCountMsg:
		delete(m.deepCohortPending, msg.key)
		if msg.err == nil {
			m.deepCohorts[msg.key] = msg.count
			m.deepCohortTrunc[msg.key] = msg.truncated
			m.deepCohortThreads[msg.key] = msg.threads
		}
		return m, m.waitProgress()
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

	// Submit work per account (a cohort can span both mailboxes). For a
	// whole-cohort action we swap in the cached server-true triage threads (the
	// set the ×N badge counts) so the action affects the whole sender, not just
	// what's in view; if the deep fetch hasn't landed yet, fall back to the
	// loaded candidates.
	if action != model.ActionKeep {
		byAccount := map[model.Account][]*model.Thread{}
		for _, c := range handled {
			byAccount[c.Thread.Account] = append(byAccount[c.Thread.Account], c.Thread)
		}
		if wholeCohort {
			group := can.Thread.SenderKey()
			for acct := range byAccount {
				if deep, ok := m.deepCohortThreads[cohortKey{account: acct, sender: group}]; ok {
					byAccount[acct] = deep
				}
			}
		}
		for acct, ts := range byAccount {
			if len(ts) == 0 {
				continue
			}
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
	kept := m.allCandidates[:0]
	for _, c := range m.allCandidates {
		if !handledIDs[c.Thread.ID] {
			kept = append(kept, c)
		}
	}
	m.allCandidates = kept
	m.refreshCandidates()
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

// refreshCandidates rebuilds the displayed candidate list from the full working
// set, applying the unread-only filter, and clamps the cursor.
func (m *appModel) refreshCandidates() {
	if m.showUnread {
		var filtered []*triage.Candidate
		for _, c := range m.allCandidates {
			if c.Thread.Unread > 0 {
				filtered = append(filtered, c)
			}
		}
		m.candidates = filtered
	} else {
		m.candidates = m.allCandidates
	}
	if m.cursor >= len(m.candidates) {
		m.cursor = len(m.candidates) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

// toggleUnread flips the unread-only filter and restarts the list at the top.
func (m *appModel) toggleUnread() {
	m.showUnread = !m.showUnread
	m.cursor = 0
	m.refreshCandidates()
}

// deepCohortKeys returns the unique (account, sender) pairs among the working
// candidates that haven't been fetched or are already in flight.
func (m *appModel) deepCohortKeys() []cohortKey {
	seen := map[cohortKey]bool{}
	var keys []cohortKey
	for _, c := range m.allCandidates {
		k := cohortKey{account: c.Thread.Account, sender: c.Thread.SenderKey()}
		if seen[k] || m.deepCohortPending[k] {
			continue
		}
		if _, done := m.deepCohorts[k]; done {
			continue
		}
		seen[k] = true
		keys = append(keys, k)
	}
	return keys
}

// deepCohortConcurrency bounds how many server-true cohort queries run at once.
const deepCohortConcurrency = 3

// fetchDeepCohorts launches an async server-true cohort count for each unseen
// sender in the list, bounded to deepCohortConcurrency concurrent queries.
func (m *appModel) fetchDeepCohorts() tea.Cmd {
	keys := m.deepCohortKeys()
	if len(keys) == 0 {
		return nil
	}
	cmds := make([]tea.Cmd, 0, len(keys))
	for _, k := range keys {
		m.deepCohortPending[k] = true
		cmds = append(cmds, m.fetchOneDeepCohort(k))
	}
	return tea.Batch(cmds...)
}

// fetchOneDeepCohort queries the server for one sender's true triage cohort
// count, bounded to deepCohortConcurrency in-flight fetches via the semaphore.
func (m *appModel) fetchOneDeepCohort(k cohortKey) tea.Cmd {
	return func() tea.Msg {
		m.deepCohortSem <- struct{}{}
		defer func() { <-m.deepCohortSem }()
		if m.engine == nil {
			return cohortCountMsg{key: k, err: fmt.Errorf("no engine")}
		}
		threads, truncated, err := m.engine.CohortThreads(m.ctx, k.account, k.sender)
		return cohortCountMsg{key: k, threads: threads, count: len(threads), truncated: truncated, err: err}
	}
}
