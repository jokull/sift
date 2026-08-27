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
	Thread    *model.Thread
	Pred      model.Prediction
	Protected bool               // today's mail — never auto-acted on
	Cohort    []*model.Thread    // other loaded threads from the same sender
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
	Protected    int
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
	Today      []*model.Thread
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
}

// New builds an engine.
func New(sources map[model.Account]accounts.Source, aiClient *ai.Client, store *state.Store) *Engine {
	order := []model.Account{}
	for a := range sources {
		order = append(order, a)
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	return &Engine{sources: sources, ai: aiClient, store: store, now: time.Now(), order: order}
}

// Load fetches, classifies and partitions the inboxes.
func (e *Engine) Load(ctx context.Context) (*Plan, error) {
	threads, warnings, err := e.loadThreads(ctx)
	if err != nil {
		return nil, err
	}
	plan := &Plan{Warnings: warnings}
	e.protectAndClassify(ctx, threads, plan)
	return plan, nil
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
			ts, err := src.ListThreads(ctx, 120)
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

func (e *Engine) protectAndClassify(ctx context.Context, threads []*model.Thread, plan *Plan) {
	plan.Stats.Loaded = len(threads)

	// Protect today's mail; keep it for display but never act on it.
	var toClassify []*model.Thread
	for _, t := range threads {
		if t.IsToday(e.now) {
			plan.Today = append(plan.Today, t)
			plan.Stats.Protected++
			continue
		}
		toClassify = append(toClassify, t)
	}

	preds := e.classify(ctx, toClassify)

	// Precompute sender cohorts over the non-protected set.
	bySender := map[string][]*model.Thread{}
	for _, t := range toClassify {
		bySender[t.SenderKey()] = append(bySender[t.SenderKey()], t)
	}

	for _, t := range toClassify {
		p := preds[t.ID]

		// Whitelisted senders keep their promotions.
		if e.store != nil && e.store.IsWhitelisted(t.SenderKey()) && p.Category == model.CategoryPromotion {
			p = model.Prediction{Category: model.CategoryPromotion, Action: model.ActionKeep,
				Confidence: 1, Reason: "whitelisted sender", SenderWide: true}
		}
		// Remembered per-sender decision overrides the default.
		if e.store != nil {
			if act, ok, _ := e.store.SenderDecision(t.SenderKey()); ok {
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
			// promotion/transactional/actionable/unknown → decision window
			cohort := bySender[t.SenderKey()]
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

	if len(uncached) > 0 && e.ai != nil {
		const chunk = 30
		for start := 0; start < len(uncached); start += chunk {
			end := start + chunk
			if end > len(uncached) {
				end = len(uncached)
			}
			batch := uncached[start:end]
			res, err := e.ai.ClassifyThreads(ctx, batch)
			if err != nil {
				// Fall back to heuristic defaults on failure; don't block triage.
				for _, t := range batch {
					preds[t.ID] = heuristic(t)
				}
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
		}
	} else if len(uncached) > 0 {
		for _, t := range uncached {
			preds[t.ID] = heuristic(t)
		}
	}
	return preds
}
