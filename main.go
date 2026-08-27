// Command sift is a TUI for triaging the Fastmail (personal) and Gmail (work)
// inboxes: prune signal from noise using DeepSeek-assisted classification and
// per-sender decisions, leaving personal mail untouched.
//
// Subcommands:
//
//	sift            run the interactive triage TUI
//	sift list       print merged inbox threads (newest first) without a TUI
//	sift doctor     verify account connectivity and print the resolved config
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jokull/sift/internal/accounts"
	"github.com/jokull/sift/internal/ai"
	"github.com/jokull/sift/internal/config"
	"github.com/jokull/sift/internal/model"
	"github.com/jokull/sift/internal/state"
	"github.com/jokull/sift/internal/triage"
	"github.com/jokull/sift/internal/ui"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "list":
			runList()
			return
		case "doctor":
			runDoctor()
			return
		case "plan":
			runPlan()
			return
		case "setup":
			if len(os.Args) > 2 && os.Args[2] == "gmail" {
				runGmailSetup()
			} else {
				runSetup()
			}
			return
		case "help", "-h", "--help":
			fmt.Println("usage: sift [list|doctor|plan|setup|--dry-run]")
			return
		}
	}
	runTUI()
}

func runList() {
	cfg, err := config.Load(config.DefaultConfigPath())
	if err != nil {
		fatal("config: %v", err)
	}
	srcs, err := accounts.New(cfg)
	if err != nil {
		fatal("accounts: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for _, src := range srcs {
		threads, err := src.ListThreads(ctx, 40)
		if err != nil {
			fatal("list %s: %v", src.Account(), err)
		}
		fmt.Printf("== %s: %d threads\n", src.Account(), len(threads))
		for _, t := range threads {
			fmt.Printf("  %-16s %-24s %s\n", t.Date.Format("2006-01-02 15:04"), truncateEmail(t.FromEmail), truncateSubj(t.Subject))
		}
	}
}

func runDoctor() {
	cfg, err := config.Load(config.DefaultConfigPath())
	if err != nil {
		fatal("config: %v", err)
	}
	fmt.Printf("deepseek: base=%s model=%s key=%s\n", cfg.DeepSeek.BaseURL, cfg.DeepSeek.Model, present(cfg.DeepSeek.APIKey))
	if cfg.Fastmail != nil {
		f := cfg.Fastmail
		fmt.Printf("fastmail: user=%s acct=%s api=%s\n", f.User, f.AccountID, f.APIURL)
		fmt.Printf("  folders: inbox=%s archive=%s receipts=%s reading=%s\n", f.Folders.Inbox, f.Folders.Archive, f.Folders.Receipts, f.Folders.Reading)
	}
	if cfg.Gmail != nil {
		g := cfg.Gmail
		fmt.Printf("gmail: acct=%s gog=%s inbox=%s receipts=%s reading=%s\n", g.Account, g.GogBin, g.InboxLabel, g.ReceiptsLabel, g.ReadingLabel)
	}
}

func present(s string) string {
	if s == "" {
		return "<none>"
	}
	if len(s) <= 8 {
		return "<set>"
	}
	return s[:8] + "…"
}

func truncateEmail(s string) string {
	if len(s) > 24 {
		return s[:24]
	}
	return s
}

func truncateSubj(s string) string {
	if len(s) > 44 {
		return s[:44] + "…"
	}
	return s
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "sift: "+format+"\n", args...)
	os.Exit(1)
}

// runTUI is filled in by the UI package; declared here so main compiles before
// the TUI exists, and defined with a real implementation once UI lands.
var runTUI = func() {
	dryRun := hasFlag(os.Args[1:], "-n", "--dry-run")
	if err := runUI(dryRun); err != nil {
		fatal("%v", err)
	}
}

func hasFlag(args []string, names ...string) bool {
	for _, a := range args {
		for _, n := range names {
			if a == n {
				return true
			}
		}
	}
	return false
}

// runSetup makes sift usable over SSH, where the macOS login keychain is often
// not accessible. It reads the Fastmail JMAP token (from the keychain when
// available) and writes it into the sift config file, so future runs no longer
// depend on the keychain.
func runSetup() {
	const user = "jokull@solberg.is"
	token, err := config.ReadFastmailToken()
	if err != nil {
		fmt.Fprintln(os.Stderr, "sift setup: could not read the Fastmail JMAP token.")
		fmt.Fprintln(os.Stderr, "  "+err.Error())
		fmt.Fprintln(os.Stderr, "  Run this in your desktop (GUI) session once, or set")
		fmt.Fprintln(os.Stderr, "  SIFT_FASTMAIL_JMAP_TOKEN=<token> in your shell profile instead.")
		os.Exit(1)
	}
	path := config.DefaultConfigPath()
	if err := config.WriteFastmailToken(path, token); err != nil {
		fatal("write config: %v", err)
	}
	fmt.Printf("Wrote Fastmail JMAP token for %s into:\n  %s\n", user, path)
	fmt.Println("sift will now use the config token and no longer needs the keychain.")
	fmt.Println("(Add ~/.config/sift/config.toml to your dotfiles backup, not the repo.)")
	runGmailSetup()
}

// runGmailSetup prepares Gmail for SSH. sift runs gog in your login (GUI) session
// via `launchctl asuser` — the same mechanism OpenClaw uses — so Gmail usually
// works over SSH with no extra credential, because the login keychain is
// unlocked. This command only needs to run if that's unavailable, in which case
// it wires gog's OAuth client creds into the config and shows the options.
func runGmailSetup() {
	path := config.DefaultConfigPath()
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sift setup gmail: "+err.Error())
	}

	if cfg.Gmail != nil && (cfg.Gmail.RefreshToken != "" || cfg.Gmail.ServiceAccount != "" || cfg.Gmail.AccessToken != "") {
		fmt.Println("Gmail auth is already configured in the config file.")
		return
	}

	fmt.Println("Gmail is accessed by running gog in your login (GUI) session")
	fmt.Println("(launchctl asuser) so it can read the login keychain — same as OpenClaw.")
	fmt.Println("This usually works over SSH with no credential. Try `./sift` first.")
	fmt.Println("")
	fmt.Println("If the login keychain is locked/absent, add ONE of these to the [gmail] section:")

	// Seed the OAuth client credentials from gog if we can.
	clientID, clientSecret, cerr := config.ReadGogClientCredentials()
	if cerr == nil && clientID != "" {
		_ = config.WriteGmailClient(path, clientID, clientSecret)
		fmt.Printf("\n  (gog's OAuth client id/secret written to %s)\n", path)
	}
	fmt.Println("")
	fmt.Println("  1) Workspace service account (recommended, no expiry):")
	fmt.Println("     - Create a service account in the Google Cloud Console (IAM & Admin > Service Accounts).")
	fmt.Println("     - In Workspace admin > Security > Access control > API controls, enable")
	fmt.Println("       'Allow domain-wide delegation' and add the SA client_id with scope")
	fmt.Println("       https://mail.google.com/ .")
	fmt.Println("     - Add to the config:")
	fmt.Println(`         [gmail]`)
	fmt.Println(`         service_account_json = "/path/to/service-account.json"`)
	fmt.Println("")
	fmt.Println("  2) OAuth refresh token (from your own Google Cloud OAuth client + consent):")
	fmt.Println(`         [gmail]`)
	fmt.Println(`         refresh_token = "..."`)
	fmt.Println("         (client_id / client_secret are written above from gog's credentials)")
	fmt.Println("")
	fmt.Println("  3) Short-lived access token (~1h, quick test):")
	fmt.Println(`         [gmail]`)
	fmt.Println(`         access_token = "..."`)
}

func runUI(dryRun bool) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	engine, store, srcs, err := buildEngine(ctx)
	if err != nil {
		return err
	}
	defer store.Close()

	// Ensure Gmail target labels exist (Receipts/Reading) before acting.
	for _, src := range srcs {
		if err := src.EnsureFolders(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "sift: %s: ensure folders: %v\n", src.Account(), err)
		}
	}

	worker := triage.NewWorker(srcs, 3, dryRun)
	worker.Start(ctx)
	defer worker.Close()

	app := ui.New(engine, worker, store, ctx)
	p := tea.NewProgram(app, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("tui: %w", err)
	}
	return nil
}

// buildEngine wires config → accounts → state → engine.
func buildEngine(ctx context.Context) (*triage.Engine, *state.Store, map[model.Account]accounts.Source, error) {
	cfg, err := config.Load(config.DefaultConfigPath())
	if err != nil {
		return nil, nil, nil, err
	}
	srcs, err := accounts.New(cfg)
	if err != nil {
		return nil, nil, nil, err
	}
	store, err := state.Open(state.DefaultPath())
	if err != nil {
		return nil, nil, nil, err
	}
	engine := triage.New(srcs, ai.New(cfg.DeepSeek), store)
	return engine, store, srcs, nil
}

// runPlan prints the classification plan without entering the TUI (verification).
func runPlan() {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	engine, store, srcs, err := buildEngine(ctx)
	if err != nil {
		fatal("setup: %v", err)
	}
	defer store.Close()
	for _, src := range srcs {
		if err := src.EnsureFolders(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "sift: %s: ensure folders: %v\n", src.Account(), err)
		}
	}
	plan, err := engine.Load(ctx)
	if err != nil {
		fatal("load: %v", err)
	}
	fmt.Printf("loaded=%d  today_untouched=%d  receipts=%d  reading=%d  candidates=%d  kept_inline=%d\n",
		plan.Stats.Loaded, plan.Stats.Protected, plan.Stats.AutoReceipts, plan.Stats.AutoReading,
		plan.Stats.Candidates, plan.Stats.KeptInline)
	for _, w := range plan.Warnings {
		fmt.Printf("warning: %s\n", w)
	}
	fmt.Printf("auto:\n")
	for _, a := range plan.Auto {
		fmt.Printf("  [%s] %s → %s  |  %s\n", a.Account, a.Action, a.Thread.FromEmail, a.Thread.Subject)
	}
	fmt.Printf("candidates (newest → oldest):\n")
	for _, c := range plan.Candidates {
		fmt.Printf("  ▶ %-11s %-10s conf=%3.0f%%  ×%d  %-14s | %s\n",
			c.Pred.Category, c.Pred.Action, c.Pred.Confidence*100, c.CohortCount(),
			truncateEmail(c.Thread.FromEmail), truncateSubj(c.Thread.Subject))
	}
}
