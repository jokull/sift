package triage

import (
	"context"
	"sync"

	"github.com/jokull/sift/internal/accounts"
	"github.com/jokull/sift/internal/model"
)

// Job is a unit of async work: move a set of threads (single account) per an
// action, with a human-facing label for the HUD.
type Job struct {
	Account model.Account
	Threads []*model.Thread
	Action  model.Action
	Label   string
}

// Progress is a HUD update for an in-flight or completed job.
type Progress struct {
	Label   string
	Account model.Account
	Total   int
	Done    int
	Failed  int
	Active  bool
	Err     string
}

// Worker executes jobs asynchronously and streams Progress updates for the HUD.
type Worker struct {
	sources     map[model.Account]accounts.Source
	concurrency int
	dryRun      bool
	sem         chan struct{}
	jobs        chan *Job
	prog        chan Progress
	wg          sync.WaitGroup
	closed      chan struct{}
	ctx         context.Context

	mu      sync.Mutex
	pending int
}

// NewWorker builds a worker bound to the given per-account sources. When
// dryRun is true, jobs report success without mutating the mailbox.
func NewWorker(sources map[model.Account]accounts.Source, concurrency int, dryRun bool) *Worker {
	if concurrency <= 0 {
		concurrency = 3
	}
	return &Worker{
		sources:     sources,
		concurrency: concurrency,
		dryRun:      dryRun,
		sem:         make(chan struct{}, concurrency),
		jobs:        make(chan *Job, 128),
		prog:        make(chan Progress, 256),
		closed:      make(chan struct{}),
	}
}

// Start launches the dispatch loop. Call once.
func (w *Worker) Start(ctx context.Context) {
	w.ctx = ctx
	go w.dispatch()
}

func (w *Worker) dispatch() {
	for {
		select {
		case <-w.closed:
			return
		case job := <-w.jobs:
			w.sem <- struct{}{}
			w.mu.Lock()
			w.pending++
			w.mu.Unlock()
			w.wg.Add(1)
			go w.run(job)
		}
	}
}

// Submit queues a job. It never blocks: on a full queue it emits a failed
// progress update so the HUD stays truthful.
func (w *Worker) Submit(job *Job) {
	if job == nil || len(job.Threads) == 0 {
		return
	}
	select {
	case w.jobs <- job:
	default:
		select {
		case w.prog <- Progress{Label: job.Label, Account: job.Account, Total: len(job.Threads), Failed: 1, Err: "queue full", Active: false}:
		default:
		}
	}
}

// Close stops dispatch and waits for in-flight jobs.
func (w *Worker) Close() {
	close(w.closed)
	w.wg.Wait()
}

// Idle reports whether any job is still queued or running.
func (w *Worker) Idle() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.pending == 0
}

// Progress returns the HUD update channel.
func (w *Worker) Progress() <-chan Progress { return w.prog }

func (w *Worker) run(job *Job) {
	defer w.wg.Done()
	defer func() {
		<-w.sem
		w.mu.Lock()
		w.pending--
		w.mu.Unlock()
	}()

	src := w.sources[job.Account]
	if src == nil {
		w.emit(Progress{Label: job.Label, Account: job.Account, Total: len(job.Threads), Failed: len(job.Threads), Err: "no source", Active: false})
		return
	}

	done, failed := 0, 0
	w.emit(Progress{Label: job.Label, Account: job.Account, Total: len(job.Threads), Active: true})
	for _, th := range job.Threads {
		if w.dryRun {
			done++
			w.emit(Progress{Label: job.Label, Account: job.Account, Total: len(job.Threads), Done: done, Failed: failed, Active: true})
			continue
		}
		err := src.Apply(w.ctx, []*model.Thread{th}, job.Action)
		if err != nil {
			failed++
			w.emit(Progress{Label: job.Label, Account: job.Account, Total: len(job.Threads), Done: done, Failed: failed, Active: true, Err: err.Error()})
		} else {
			done++
			w.emit(Progress{Label: job.Label, Account: job.Account, Total: len(job.Threads), Done: done, Failed: failed, Active: true})
		}
	}
	w.emit(Progress{Label: job.Label, Account: job.Account, Total: len(job.Threads), Done: done, Failed: failed, Active: false})
}

func (w *Worker) emit(p Progress) {
	select {
	case w.prog <- p:
	default:
	}
}
