// Package triage glues the mail accounts, the classifier, and local state into a
// per-sender decision plan plus auto-actions, newest first.
package triage

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/jokull/sift/internal/accounts"
	"github.com/jokull/sift/internal/ai"
	"github.com/jokull/sift/internal/model"
	"github.com/jokull/sift/internal/state"
)

// Candidate is a thread that needs a decision, carrying its prediction and the
// sender cohort used for "how many would be archived" context.
type Candidate struct {
	Thread *model.Thread
	Pred   model.Prediction
	Cohort []*model.Thread // other loaded threads from the same sender
}

// CohortCount returns how many threads share this candidate's sender.
func (c *Candidate) CohortCount() int { return len(c.Cohort) }

// AutoJob is an automatic action (receipts/newsletters) to run without a human
// decision.
type AutoJob struct {
	Account model.Account
	Thread  *model.Thread
	Action  model.Action
}

// Result pairs a thread with how to act on it, used for both auto and user jobs.
type Result struct {
	Thread *model.Thread
	Action model.Action
}

// Stats summarizes a load.
type Stats struct {
	Loaded       int
	AutoReceipts int
	AutoReading  int
	Candidates   int
	KeptInline   int
	Unclassified int
}

// Plan is the outcome of a load: what needs a decision, what runs automatically,
// and what stays untouched.
type Plan struct {
	Candidates []*Candidate
	Auto       []AutoJob
	Stats      Stats
	Warnings   []string // non-fatal per-account problems (e.g. gog keychain over SSH)
}

// Engine loads and classifies inboxes from multiple sources.
type Engine struct {
	sources map[model.Account]accounts.Source
	ai      *ai.Client
	store   *state.Store
	now     time.Time
	order   []model.Account
	boot    chan string // boot-stage progress updates (consumed by the TUI)
}

// New builds an engine.
func New(sources map[model.Account]accounts.Source, aiClient *ai.Client, store *state.Store) *Engine {
	order := []model.Account{}
	for a := range sources {
		order = append(order, a)
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	return &Engine{sources: sources, ai: aiClient, store: store, now: time.Now(), order: order, boot: make(chan string, 64)}
}

// Progress returns a channel of boot-stage updates (e.g. "fetching fastmail…").
// It is drained by the TUI to show work happening while loading.
func (e *Engine) Progress() <-chan string { return e.boot }

// report emits a non-blocking boot progress update. The buffered channel lets
// progress lag the UI without ever blocking a load.
func (e *Engine) report(format string, args ...any) {
	if e.boot == nil {
		return
	}
	select {
	case e.boot <- fmt.Sprintf(format, args...):
	default:
	}
}

// Load fetches, classifies and partitions the inboxes.
func (e *Engine) Load(ctx context.Context) (*Plan, error) {
	threads, warnings, err := e.loadThreads(ctx)
	if err != nil {
		return nil, err
	}
	plan := &Plan{Warnings: warnings}
	e.classifyAndBuild(ctx, threads, plan)
	return plan, nil
}

// Messages fetches a thread's messages (plaintext bodies) from its account
// source, for the drilldown view.
func (e *Engine) Messages(ctx context.Context, thread *model.Thread) ([]*model.Message, error) {
	if thread == nil {
		return nil, nil
	}
	src := e.sources[thread.Account]
	if src == nil {
		return nil, fmt.Errorf("no account source for %s", thread.Account)
	}
	return src.ListMessages(ctx, thread)
}

func (e *Engine) loadThreads(ctx context.Context) ([]*model.Thread, []string, error) {
	type res struct {
		acct model.Account
		ts   []*model.Thread
		err  error
	}
	results := make(chan res, len(e.sources))
	var wg sync.WaitGroup
	for _, acct := range e.order {
		src := e.sources[acct]
		if src == nil {
			continue
		}
		wg.Add(1)
		go func(acct model.Account, src accounts.Source) {
			defer wg.Done()
			e.report("fetching %s…", acct)
			ts, err := src.ListThreads(ctx, 200)
			e.report("loaded %s: %d threads", acct, len(ts))
			results <- res{acct, ts, err}
		}(acct, src)
	}
	wg.Wait()
	close(results)

	var all []*model.Thread
	var warnings []string
	for r := range results {
		if r.err != nil {
			warnings = append(warnings, fmt.Sprintf("%s unavailable: %v", r.acct, r.err))
			continue
		}
		all = append(all, r.ts...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Date.After(all[j].Date) })
	if len(all) == 0 && len(warnings) > 0 {
		return nil, warnings, fmt.Errorf("no accounts loaded")
	}
	return all, warnings, nil
}

// classifyAndBuild classifies every thread and partitions them into the plan:
// receipts/newsletters auto-pluck, keep stays in the inbox, and everything else
// becomes a decision candidate (already newest first).
func (e *Engine) classifyAndBuild(ctx context.Context, threads []*model.Thread, plan *Plan) {
	plan.Stats.Loaded = len(threads)

	preds := e.classify(ctx, threads)

	// Precompute sender cohorts over the loaded set, keyed by the exact sender so
	// a cohort spans the whole loaded inbox (not just the rows visible in the
	// list). Each cohort is scoped to the candidate's category so a bulk action
	// never drags in unmatching mail (e.g. receipts next to promos).
	bySender := map[string][]*model.Thread{}
	for _, t := range threads {
		bySender[t.SenderKey()] = append(bySender[t.SenderKey()], t)
	}

	for _, t := range threads {
		p := preds[t.ID]
		key := t.SenderKey()

		// Whitelisted senders keep their promotions.
		if e.store != nil && e.store.IsWhitelisted(key) && p.Category == model.CategoryPromotion {
			p = model.Prediction{Category: model.CategoryPromotion, Action: model.ActionKeep,
				Confidence: 1, Reason: "whitelisted sender", SenderWide: true}
		}
		// Remembered per-sender decision overrides the default.
		if e.store != nil {
			if act, ok, _ := e.store.SenderDecision(key); ok {
				if p.Category == model.CategoryPromotion || p.Category == model.CategoryTransactional {
					switch act {
					case model.ActionKeep, model.ActionArchive, model.ActionUnsubscribe:
						p = model.Prediction{Category: p.Category, Action: act,
							Confidence: 1, Reason: "saved sender decision", SenderWide: true}
					}
				}
			}
		}
		// Actionable alerts older than 24h are bulk-archived.
		if p.Category == model.CategoryActionable && e.now.Sub(t.Date) > 24*time.Hour {
			p.Action = model.ActionArchive
		}

		switch p.Category {
		case model.CategoryReceipt:
			plan.Auto = append(plan.Auto, AutoJob{Account: t.Account, Thread: t, Action: model.ActionReceipts})
			plan.Stats.AutoReceipts++
			plan.Stats.KeptInline++
		case model.CategoryNewsletter:
			plan.Auto = append(plan.Auto, AutoJob{Account: t.Account, Thread: t, Action: model.ActionReading})
			plan.Stats.AutoReading++
			plan.Stats.KeptInline++
		case model.CategoryKeep:
			plan.Stats.KeptInline++
			// personal/meaningful — never asked, stays in inbox
		default:
			// promotion/transactional/actionable/unknown → decision window.
			// Cohort = the sender's triage threads across categories, so a bulk
			// decision acts on the whole sender (not just one category). Auto
			// actions (receipts/newsletters) and keep-stays-inbox are excluded —
			// they are handled on their own, never dragged into an archive.
			cohort := make([]*model.Thread, 0, len(bySender[t.SenderKey()]))
			for _, ct := range bySender[t.SenderKey()] {
				if isTriageCategory(preds[ct.ID].Category) {
					cohort = append(cohort, ct)
				}
			}
			plan.Candidates = append(plan.Candidates, &Candidate{
				Thread: t, Pred: p, Cohort: cohort,
			})
			plan.Stats.Candidates++
		}
	}

	// Candidates newest-first (they were already sorted globally, and we append
	// in that order, so they are).
	sort.Slice(plan.Candidates, func(i, j int) bool {
		return plan.Candidates[i].Thread.Date.After(plan.Candidates[j].Thread.Date)
	})
}

// isTriageCategory reports whether a category produces a decision candidate
// (as opposed to an auto action or keep-stays-in-inbox).
func isTriageCategory(c model.Category) bool {
	switch c {
	case model.CategoryReceipt, model.CategoryNewsletter, model.CategoryKeep:
		return false
	}
	return true
}

// classify returns a prediction per thread, using cache first then one batched
// DeepSeek call for uncached threads.
func (e *Engine) classify(ctx context.Context, threads []*model.Thread) map[string]model.Prediction {
	preds := map[string]model.Prediction{}
	var uncached []*model.Thread
	for _, t := range threads {
		if e.store != nil {
			if p, ok, err := e.store.Classification(string(t.Account), t.ID); err == nil && ok {
				preds[t.ID] = p
				continue
			}
		}
		uncached = append(uncached, t)
	}

	if len(uncached) == 0 {
		return preds
	}
	e.report("classifying %d threads…", len(uncached))
	if e.ai != nil {
		const chunk = 30
		total := len(uncached)
		done := 0
		for start := 0; start < total; start += chunk {
			end := start + chunk
			if end > total {
				end = total
			}
			batch := uncached[start:end]
			res, err := e.ai.ClassifyThreads(ctx, batch)
			if err != nil {
				// Fall back to heuristic defaults on failure; don't block triage.
				for _, t := range batch {
					preds[t.ID] = heuristic(t)
				}
				done += len(batch)
				e.report("classifying %d/%d", done, total)
				continue
			}
			for _, t := range batch {
				p, ok := res[t.ID]
				if !ok || p.Category == model.CategoryUnknown || p.Confidence < 0.4 {
					p = heuristic(t)
				}
				preds[t.ID] = p
				if e.store != nil {
					_ = e.store.SaveClassification(string(t.Account), t.ID, p)
				}
			}
			done += len(batch)
			e.report("classifying %d/%d", done, total)
		}
	} else {
		for _, t := range uncached {
			preds[t.ID] = heuristic(t)
		}
	}
	return preds
}
