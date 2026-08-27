package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/jokull/sift/internal/model"
	"github.com/jokull/sift/internal/triage"
)

var (
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	subStyle     = lipgloss.NewStyle().Faint(true)
	headerStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	cursorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("229")).Background(lipgloss.Color("90"))
	dimStyle     = lipgloss.NewStyle().Faint(true)
	accentStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	warnStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))

	actionColor = map[model.Action]lipgloss.Style{
		model.ActionKeep:       lipgloss.NewStyle().Foreground(lipgloss.Color("42")),
		model.ActionArchive:    lipgloss.NewStyle().Foreground(lipgloss.Color("80")),
		model.ActionUnsubscribe: lipgloss.NewStyle().Foreground(lipgloss.Color("213")),
		model.ActionReceipts:   lipgloss.NewStyle().Foreground(lipgloss.Color("226")),
		model.ActionReading:    lipgloss.NewStyle().Foreground(lipgloss.Color("39")),
	}
	accountStyle = map[model.Account]lipgloss.Style{
		model.AccountFastmail: lipgloss.NewStyle().Foreground(lipgloss.Color("214")),
		model.AccountGmail:    lipgloss.NewStyle().Foreground(lipgloss.Color("105")),
	}
)

func (m *appModel) viewLoading() string {
	if m.loadingMsg == "" {
		m.loadingMsg = "Loading…"
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		titleStyle.Render("sift — inbox triage"),
		"",
		dimStyle.Render(m.loadingMsg),
	)
}

func (m *appModel) View() string {
	if !m.loaded {
		return m.viewLoading()
	}
	var b strings.Builder
	b.WriteString(m.viewHeader())
	b.WriteString("\n\n")
	if m.detail != nil {
		b.WriteString(m.detail.render())
		b.WriteString("\n\n")
	}
	b.WriteString(m.viewList())
	b.WriteString("\n")
	b.WriteString(m.viewHUD())
	b.WriteString("\n")
	b.WriteString(m.viewFooter())

	return b.String()
}

func (m *appModel) viewHeader() string {
	line := fmt.Sprintf("%s  %s", titleStyle.Render("sift"), subStyle.Render("inbox triage — newest → oldest"))
	stats := m.status
	if stats == "" {
		stats = planStatus(&triage.Plan{Stats: m.stats})
	}
	return line + "\n" + dimStyle.Render(stats)
}

func (m *appModel) viewList() string {
	if len(m.candidates) == 0 {
		head := fmt.Sprintf("All clear — %d to review.\n", len(m.candidates))
		head += dimStyle.Render("   (today's mail is left untouched)")
		if len(m.today) > 0 {
			head += dimStyle.Render(fmt.Sprintf("\n   %d today — untouched.", len(m.today)))
		}
		return head
	}

	// Column widths.
	senderW, subjW, cohortW := 24, 44, 5
	rows := make([]string, len(m.candidates))
	for i, c := range m.candidates {
		cur := ""
		if i == m.cursor {
			cur = "▸"
		} else {
			cur = " "
		}
		badge := actionColor[c.Pred.Action].Render(fmt.Sprintf("%-10s", actionName(c.Pred.Action)))
		sender := truncate(c.Thread.FromEmail, senderW)
		subj := truncate(c.Thread.Subject, subjW)
		acct := accountStyle[c.Thread.Account].Render(accountTag(c.Thread.Account))
		senderCell := pad(sender, senderW)
		if i == m.cursor {
			senderCell = cursorStyle.Render(senderCell)
		} else {
			senderCell = dimStyle.Render(senderCell)
		}
		age := fmt.Sprintf("%-6s", ageString(c.Thread.Date, m.now))
		cohort := fmt.Sprintf("×%d", c.CohortCount())
		cohort = dimStyle.Render(fmt.Sprintf("%-*s", cohortW, cohort))
		row := cur + " " + badge + " " + senderCell + "  " + subj + "  " + acct + "  " + age + "  " + cohort
		rows[i] = row
	}
	return strings.Join(rows, "\n")
}

func (m *appModel) viewHUD() string {
	lines := []string{}
	labels := []string{}
	// stable-ish ordering: running first, then completed.
	for l := range m.progress {
		labels = append(labels, l)
	}
	// sort labels deterministically
	for i := 1; i < len(labels); i++ {
		for j := i; j > 0 && labels[j] < labels[j-1]; j-- {
			labels[j], labels[j-1] = labels[j-1], labels[j]
		}
	}
	for _, l := range labels {
		p := m.progress[l]
		bar := hudBar(p)
		lines = append(lines, bar)
	}
	if len(lines) == 0 {
		return ""
	}
	return dimStyle.Render("── actions ──") + "\n" + strings.Join(lines, "\n")
}

func hudBar(p triage.Progress) string {
	icon := "✓"
	color := lipgloss.Color("42")
	if p.Active {
		icon = "…"
		color = lipgloss.Color("39")
	} else if p.Failed > 0 {
		icon = "✗"
		color = lipgloss.Color("214")
	}
	acct := accountStyle[p.Account].Render(accountTag(p.Account))
	base := fmt.Sprintf("%s %s %s %s", lipgloss.NewStyle().Foreground(color).Render(icon), acct, p.Label, fmt.Sprintf("%d/%d", p.Done, p.Total))
	if !p.Active && p.Failed > 0 {
		base += dimStyle.Render(fmt.Sprintf(" (%d failed: %s)", p.Failed, truncate(p.Err, 40)))
	}
	return base
}

func (m *appModel) viewFooter() string {
	return dimStyle.Render("↑/↓ or j/k move · ⏎ detail · a archive · u unsubscribe · r receipts · n reading · s keep · A/U/R/N whole sender · q quit")
}

func accountTag(a model.Account) string {
	switch a {
	case model.AccountFastmail:
		return "[f]"
	case model.AccountGmail:
		return "[g]"
	}
	return "[?]"
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

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return string(r)
	}
	if n <= 1 {
		return string(r[:1])
	}
	return string(r[:n-1]) + "…"
}

func pad(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}
