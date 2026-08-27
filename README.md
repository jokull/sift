# sift

A TUI for email cleanup triage across your two inboxes — Fastmail (personal,
`jokull@solberg.is`) and Gmail (work, `jokull@triptojapan.com`). It prunes
signal from noise, newest → oldest, so your inbox holds personal and meaningful
communication instead of a growing pile of receipts, newsletters, and
promotions.

`sift` **reuses the token/config setup already present from OpenClaw**: the
Fastmail JMAP token (macOS Keychain item `fastmail-jmap`), the authenticated
`gog` Gmail CLI, and the DeepSeek API key in `~/.openclaw/.env`. No secrets are
copied into the repository.

## What it does

Given a merged view of both inboxes, `sift` classifies every thread and applies
your rules:

| Category | Behaviour |
| --- | --- |
| **Today's mail** | never touched — left in the inbox |
| **Receipts** (invoices, orders, payments) | auto-pluck → `Receipts` folder |
| **Newsletters** (Substack, Figma, digests) | auto-pluck → `Reading` folder |
| **Promotions** | decision per sender → archive / unsubscribe / whitelist |
| **Transactional** (notifications, codes) | decision per sender → archive |
| **Actionable** (Sentry, errors, review) | shown for your eyes; bulk-archive after 24h |
| **Personal / meaningful** | never asked about — stays in inbox |

Classification runs **DeepSeek v4 Flash** with reasoning disabled, so a batch of
dozens of threads is classified in ~2s at minimal cost. Results are cached in
`~/.local/share/sift/state.db`, so re-runs are instant. When DeepSeek is
unavailable, `sift` falls back to deterministic heuristics that never archive
mail it isn't sure about.

## Usage

```bash
sift                 # run the interactive triage TUI
sift --dry-run       # preview all actions without mutating any mailbox
sift plan            # print the classification plan (read-only, no TUI)
sift doctor          # print the resolved config / account connectivity
sift list            # print the merged inbox threads (read-only)
```

## TUI

The list shows threads needing a decision, newest first. Each row is a decision
**window**: its AI-suggested action, sender, subject, account, age, and how many
threads from that sender would be affected (`×N`).

```
┌──────────────────────────────────────────────────────────────┐
│ ▸ archive    no_reply@email.apple.com  Your app, Lyklaborð… [f]  14h  ×4 │
└──────────────────────────────────────────────────────────────┘
```

Press `⏎` on a row for the full decision window (sender, cohort "would be
affected" context, the AI's reasoning), then pick an action. Async actions run
in the background and stream into a **HUD** at the bottom with live status.

### Keys

| Key | Action |
| --- | --- |
| `↑`/`↓` or `j`/`k` | navigate |
| `⏎` / `space` | open the decision window |
| `a` / `u` / `r` / `n` | archive / unsubscribe / receipts / reading (this thread) |
| `s` | keep (whitelist sender for promotions; stays in inbox) |
| `A` / `U` / `R` / `N` | same, applied to **every** thread from that sender |
| `x` (in window) | apply the AI default action to the whole sender cohort |
| `b` / `esc` | back |
| `q` / `ctrl-c` | quit |

## Setup / config

Auto-discovery finds everything, so no config is needed. To override defaults,
copy `config.example.toml` to `~/.config/sift/config.toml` (or `$XDG_CONFIG_HOME`).
`sift doctor` prints what it resolved.

## How the AI is used (keeping cost + latency low)

1. **One batched classification call** covers up to ~30 threads at a time
   (subject + sender + Gmail category hint → category/action/confidence/reason).
2. **Reasoning is disabled** (`thinking: {type:"disabled"}`), which drops token
   use ~5× and latency to ~2s per batch.
3. **Per-thread cache** avoids re-classifying on re-runs.
4. **Per-sender decisions** are remembered, so once you archive/unsubscribe a
   sender, their mail is handled automatically next time.

## Architecture

```
cmd … main.go                 CLI entry (list/doctor/plan/TUI)
internal/config              OpenClaw-derived config discovery + overrides
internal/jmap                minimal Fastmail JMAP client
internal/accounts            Source interface + Fastmail + Gmail (gog) impls
internal/ai                  DeepSeek client + batched classifier
internal/triage              engine (rules, cohorts, plan) + async worker (HUD)
internal/state               SQLite cache + sender decisions/whitelist
internal/ui                  bubbletea TUI (list / decision window / HUD)
```

## Build

```bash
go build -o sift .
```

## Safety

- **Today's mail is never touched.**
- Unknown / low-confidence threads are **never archived** — they default to
  keep.
- Gmail target labels (`Receipts`, `Reading`) are created automatically if
  missing.
- Run `sift --dry-run` to preview actions before they land.
