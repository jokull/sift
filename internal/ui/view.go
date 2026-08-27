package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/jokull/sift/internal/model"
	"github.com/jokull/sift/internal/triage"
)

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	subStyle    = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("244"))
	statsStyle  = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("240"))
	sectStyle   = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("238"))
	ruleStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("236"))
	headerStyle = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("240"))
	selStyle    = lipgloss.NewStyle().Background(lipgloss.Color("238"))
	dimStyle    = lipgloss.NewStyle().Faint(true)
	accentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	warnStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
)

// actionBadge maps an action to a short, fixed-width badge label.
var actionBadge = map[model.Action]string{
	model.ActionKeep:        "keep",
	model.ActionArchive:     "archive",
	model.ActionUnsubscribe: "unsub",
	model.ActionReceipts:    "receipts",
	model.ActionReading:     "reading",
}

var actionColor = map[model.Action]lipgloss.Style{
	model.ActionKeep:        lipgloss.NewStyle().Foreground(lipgloss.Color("42")),
	model.ActionArchive:     lipgloss.NewStyle().Foreground(lipgloss.Color("80")),
	model.ActionUnsubscribe: lipgloss.NewStyle().Foreground(lipgloss.Color("213")),
	model.ActionReceipts:    lipgloss.NewStyle().Foreground(lipgloss.Color("226")),
	model.ActionReading:     lipgloss.NewStyle().Foreground(lipgloss.Color("39")),
}

var accountStyle = map[model.Account]lipgloss.Style{
	model.AccountFastmail: lipgloss.NewStyle().Foreground(lipgloss.Color("214")),
	model.AccountGmail:    lipgloss.NewStyle().Foreground(lipgloss.Color("105")),
}

func (m *appModel) viewLoading() string {
	if m.loadingMsg == "" {
		m.loadingMsg = "Loading…"
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		titleStyle.Render("sift")+"  "+subStyle.Render("inbox triage"),
		"",
		dimStyle.Render(m.loadingMsg),
	)
}

func (m *appModel) View() string {
	if !m.loaded {
		return m.viewLoading()
	}
	if m.width <= 0 {
		m.width = 100
	}
	height := m.height
	if height <= 0 {
		height = 40
	}

	header := m.viewHeader()
	hud := m.viewStatus()
	help := ""
	if m.help {
		help = m.helpPanel()
	}

	footerLines := 1
	statusLines := lineCount(hud)
	gap := 2
	listSpace := height - lineCount(header) - lineCount(help) - statusLines - footerLines - gap
	if listSpace < 1 {
		listSpace = 1
	}

	var b strings.Builder
	b.WriteString(header)
	if help != "" {
		b.WriteString("\n")
		b.WriteString(help)
	}
	b.WriteString("\n\n")

	// Detail window gets the top slice of the list area; the list scrolls below.
	bodyLines := listSpace
	if m.detail != nil {
		detailText := m.detail.render()
		dl := lineCount(detailText)
		if dl > bodyLines-2 {
			dl = bodyLines - 2
		}
		if dl < 1 {
			dl = 1
		}
		b.WriteString(detailText)
		b.WriteString("\n\n")
		bodyLines -= dl + 1
	}
	b.WriteString(m.viewListWindow(bodyLines))

	if hud != "" {
		b.WriteString("\n")
		b.WriteString(hud)
	}
	b.WriteString("\n")
	b.WriteString(m.viewFooter())
	return b.String()
}

func (m *appModel) helpPanel() string {
	rows := []string{
		"↑/↓ or j/k   navigate",
		"⏎ / space    open decision window",
		"a            archive this thread",
		"u            unsubscribe sender (archive + remember)",
		"r            move to Receipts",
		"n            move to Reading",
		"s            keep (whitelist sender; stays in inbox)",
		"A/U/R/N      same, applied to every thread from that sender",
		"x (window)   apply AI default action to whole sender cohort",
		"b / esc      back · q quit",
	}
	return sectStyle.Render("── help ──\n") + strings.Join(rows, "\n")
}

// viewHeader renders the title, plan summary and any warnings; it never changes
// with transient progress (that belongs to the HUD below).
func (m *appModel) viewHeader() string {
	title := titleStyle.Render("sift") + "  " + subStyle.Render("inbox triage — newest → oldest")
	stats := fmt.Sprintf("%d to review · %d auto-pluck (receipts) · %d auto-read · %d today untouched",
		len(m.candidates), m.stats.AutoReceipts, m.stats.AutoReading, len(m.today))
	line := title + "\n" + statsStyle.Render(stats)
	if len(m.warnings) > 0 {
		line += "\n" + warnStyle.Render("⚠ "+m.warnings[0])
	}
	return line
}

func (m *appModel) viewListWindow(n int) string {
	if len(m.candidates) == 0 {
		msg := "All clear — nothing needs a decision."
		if len(m.today) > 0 {
			msg = fmt.Sprintf("All clear — nothing needs a decision. (%d today left untouched.)", len(m.today))
		}
		return dimStyle.Render(msg)
	}
	if n <= 0 {
		n = 1
	}

	// Column widths (display width). Subject flexes to use the terminal.
	senderW := 22
	badgeW := 9
	acctW := 4
	ageW := 7
	cohortW := 6
	// fixed = badge + sender + account + age + cohort + column gaps (4 spaces each ≈ 5 gaps)
	gaps := 4 * 5
	subjW := m.width - (badgeW + senderW + acctW + ageW + cohortW + gaps)
	if subjW < 24 {
		subjW = 24
	}
	if subjW > 88 {
		subjW = 88
	}

	// Reserve 2 lines for the column header + rule; the rest are data rows.
	dataRows := n - 2
	if dataRows < 1 {
		dataRows = 1
	}

	rows := make([]string, 0, dataRows+2)
	headerLine := "  " + padTo("", badgeW) + "  " + padTo("SENDER", senderW) + "  " +
		padTo("SUBJECT", subjW) + "  " + padTo("ACCT", acctW) + "  " + padTo("AGE", ageW) + "  " + padTo("COHORT", cohortW)
	rows = append(rows, sectStyle.Render(headerLine))
	rows = append(rows, ruleStyle.Render(strings.Repeat("─", m.width)))

	// Window the data rows around the cursor so the list scrolls with selection.
	total := len(m.candidates)
	start := m.cursor - dataRows/2
	if start < 0 {
		start = 0
	}
	if start+dataRows > total {
		start = total - dataRows
	}
	if start < 0 {
		start = 0
	}
	end := start + dataRows
	if end > total {
		end = total
	}

	for i := start; i < end; i++ {
		c := m.candidates[i]
		sel := i == m.cursor

		badge := actionBadge[c.Pred.Action]
		if badge == "" {
			badge = "?"
		}
		badgeCell := actionColor[c.Pred.Action].Render(fit(badge, badgeW))
		senderCell := fit(c.Thread.FromEmail, senderW)
		subjCell := fit(c.Thread.Subject, subjW)
		acctCell := accountStyle[c.Thread.Account].Render(fit(accountTag(c.Thread.Account), acctW))
		ageCell := fit(ageString(c.Thread.Date, m.now), ageW)
		cohortCell := dimStyle.Render(fit(fmt.Sprintf("×%d", c.CohortCount()), cohortW))

		line := "  " + badgeCell + "  " + senderCell + "  " + subjCell + "  " + acctCell + "  " + ageCell + "  " + cohortCell
		if sel {
			line = "▸" + line[1:]
			line = selStyle.Render(line)
		} else {
			line = " " + line[1:]
		}
		rows = append(rows, line)
	}
	return strings.Join(rows, "\n")
}

// viewStatus renders the live task footer: an aggregate counter plus one line
// per tracked action showing its state (… in progress / ✓ confirmed / ✗ failed)
// with a done/total counter and a progress bar. In-progress and failed tasks
// surface first.
func (m *appModel) viewStatus() string {
	var active, failed, done []string
	inP, f, d := 0, 0, 0
	for label, p := range m.progress {
		switch {
		case p.Active:
			inP++
			active = append(active, label)
		case p.Failed > 0:
			f++
			failed = append(failed, label)
		default:
			d++
			done = append(done, label)
		}
	}
	if inP+f+d == 0 {
		return ""
	}
	sortStrings(active)
	sortStrings(failed)
	sortStrings(done)

	lines := []string{sectStyle.Render(fmt.Sprintf("── tasks · %d in progress · %d confirmed · %d failed ──", inP, d, f))}
	for _, l := range active {
		lines = append(lines, taskLine(m.progress[l], l))
	}
	for _, l := range failed {
		lines = append(lines, taskLine(m.progress[l], l))
	}
	for _, l := range done {
		lines = append(lines, taskLine(m.progress[l], l))
	}
	return strings.Join(lines, "\n")
}

func taskLine(p triage.Progress, label string) string {
	icon := "✓"
	color := lipgloss.Color("42")
	state := "confirmed"
	if p.Active {
		icon = "…"
		color = lipgloss.Color("39")
		state = "in progress"
	} else if p.Failed > 0 {
		icon = "✗"
		color = lipgloss.Color("214")
		state = "failed"
	}
	acct := accountStyle[p.Account].Render(fit(accountTag(p.Account), 4))
	// progress bar over the done+failed fraction.
	width := 12
	ratio := 0.0
	if p.Total > 0 {
		ratio = float64(p.Done+p.Failed) / float64(p.Total)
	}
	filled := int(ratio * float64(width))
	if filled > width {
		filled = width
	}
	bar := lipgloss.NewStyle().Foreground(color).Render(strings.Repeat("█", filled) + strings.Repeat("░", width-filled))
	counter := fmt.Sprintf("%d/%d", p.Done, p.Total)
	line := fmt.Sprintf("%s %s  %-22s %s  %s  %s",
		lipgloss.NewStyle().Foreground(color).Render(icon),
		acct,
		fit(label, 22),
		fit(state, 12),
		counter,
		bar,
	)
	if !p.Active && p.Failed > 0 {
		line += "  " + dimStyle.Render(truncateRunewidth(p.Err, 22))
	}
	return line
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func (m *appModel) viewFooter() string {
	return dimStyle.Render("j/k move · ⏎ detail · a/u/r/n act · A/U/R/N cohort · s keep · q quit · ? help")
}

func accountTag(a model.Account) string {
	switch a {
	case model.AccountFastmail:
		return "f"
	case model.AccountGmail:
		return "g"
	}
	return "?"
}

func ageString(t, now time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := now.Sub(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// fit truncates s to display width w (adding "…" when cut) and pads to exactly w
// using rune width, so multibyte text and ANSI don't misalign the columns.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	tw := runewidth.StringWidth(s)
	if tw <= w {
		return s + strings.Repeat(" ", w-tw)
	}
	t := runewidth.Truncate(s, w-1, "…")
	return t + strings.Repeat(" ", w-runewidth.StringWidth(t))
}

func padTo(s string, w int) string { return fit(s, w) }

func lineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

func truncateRunewidth(s string, n int) string {
	if runewidth.StringWidth(s) <= n {
		return s
	}
	return runewidth.Truncate(s, n-1, "…")
}
