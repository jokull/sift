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

**Unsubscribe is real.** When you choose `unsubscribe` on a promotion/newsletter,
`sift` reads the message's `List-Unsubscribe` header and performs a best-effort
RFC 8058 **one-click unsubscribe** (POST, with `List-Unsubscribe-Post`), or a
plain GET on the unsubscribe link, then archives the thread and remembers the
sender. It uses each account's own mechanism (gog for Gmail, JMAP for Fastmail),
and the result is shown in the actions HUD.

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

### Running over SSH

macOS login keychain is often **not accessible from an SSH session**, so the
keychain read can fail with `exit 36` (`errSecInteractionNotAllowed`). To make
`sift` work over SSH:

```bash
sift setup        # once, from a desktop/GUI session: writes the Fastmail JMAP
                  # token into ~/.config/sift/config.toml (mode 0600)
```

After that, `sift` reads the Fastmail token from the config file and no longer
needs the keychain.

**Gmail over SSH** is handled by a small **gog bridge daemon**: it runs `gog`
in your login (GUI) session where the login keychain is unlocked — the exact
mechanism OpenClaw uses — and `sift` talks to it headlessly over a unix socket.
So Gmail works from an SSH session with no extra credential:

```bash
sift setup daemon                                   # install the login-session bridge (LaunchAgent)
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/ai.jokull.sift.gogd.plist
```

`sift` auto-detects the bridge socket (`~/.sift/gogd.sock`) and uses it; you can
also run the daemon in the foreground with `sift daemon`. If the login keychain
is unavailable (or on non-macOS), `sift` falls back to a clear warning and you
can give it its own Gmail token via `sift setup gmail`, then add **one** of these
under `[gmail]` in `~/.config/sift/config.toml`:

- **Workspace service account (recommended, no expiry):** create a service
  account, enable domain-wide delegation for its client_id with scope
  `https://mail.google.com/`, then set
  `service_account_json = "/path/to/service-account.json"`.
- **OAuth refresh token:** set `refresh_token = "..."` (uses the `client_id` /
  `client_secret` that `sift setup gmail` wrote).
- **Short-lived access token (~1h, quick test):** set `access_token = "..."`.

`sift` mints an access token from any of these and hands it to `gog` via
`GOG_ACCESS_TOKEN`.

The values in `~/.config/sift/config.toml` are machine-local; back them up with
your dotfiles, not the repo.

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
