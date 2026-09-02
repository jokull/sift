package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/jokull/sift/internal/model"
	"github.com/jokull/sift/internal/triage"
	"github.com/mattn/go-runewidth"
)

// Drilldown depths.
const (
	levelList     = 0 // candidate list (the triage list)
	levelThreads  = 1 // the selected candidate's sender-cohort threads
	levelMessages = 2 // a thread's messages and their (plain) contents
)

// Column widths for the compacted sidebar layout. The deeper you drill, the
// narrower the previous columns become so the focused column stays readable.
func (m *appModel) drillWidths() (c1, c2, c3 int) {
	total := m.width
	if total <= 0 {
		total = 100
	}
	switch m.level {
	case levelMessages:
		c1 = clamp(total/6, 16, 28)
		c2 = clamp(total/4, 22, 40)
		c3 = total - c1 - c2 - 2
	case levelThreads:
		c1 = clamp(total/4, 24, 42)
		c2 = total - c1 - 1
		c3 = 0
	default:
		c1 = total
		c2 = 0
		c3 = 0
	}
	if c3 < 20 {
		c3 = 20
	}
	return c1, c2, c3
}

// drillSubtitle is the top-right breadcrumb: what column you are focused in.
func (m *appModel) drillSubtitle() string {
	switch m.level {
	case levelThreads:
		return "candidates ▸ threads"
	case levelMessages:
		return "candidates ▸ threads ▸ messages"
	}
	return ""
}

// renderDrill renders the whole drilldown body for the current depth.
func (m *appModel) renderDrill(height int) string {
	bodyH := m.bodyHeight(height)
	c1, c2, c3 := m.drillWidths()

	cols := []string{}
	if m.level >= levelList {
		cols = append(cols, m.renderCandidateColumn(c1, bodyH))
	}
	if m.level >= levelThreads {
		cols = append(cols, m.renderThreadColumn(c2, bodyH))
	}
	if m.level >= levelMessages {
		cols = append(cols, m.renderMessageColumn(c3, bodyH))
	}
	return joinColumns(cols)
}

// bodyHeight computes the number of lines available for the column body (frame
// minus header/footer/HUD reserved elsewhere).
func (m *appModel) bodyHeight(height int) int {
	if height <= 0 {
		height = 40
	}
	// Leave room for header (1 title + 1 stats + 1 warning ≈ 3), heading rule,
	// the task HUD, and the footer.
	n := height - 4 - 1 - 1
	if n < 4 {
		n = 4
	}
	return n
}

// joinColumns renders side-by-side columns with a dim `│` separator.
func joinColumns(cols []string) string {
	if len(cols) == 0 {
		return ""
	}
	widths := make([]int, len(cols))
	rows := 0
	for i, c := range cols {
		widths[i] = lineWidth(c)
		if n := lineCount(c); n > rows {
			rows = n
		}
	}
	lines := make([]string, rows)
	for r := 0; r < rows; r++ {
		var b strings.Builder
		for i, c := range cols {
			cl := strings.Split(c, "\n")
			cell := ""
			if r < len(cl) {
				cell = cl[r]
			}
			b.WriteString(fitAnsi(cell, widths[i]))
			if i < len(cols)-1 {
				b.WriteString(dimStyle.Render("│"))
			}
		}
		lines[r] = strings.TrimRight(b.String(), " ")
	}
	return strings.Join(lines, "\n")
}

// renderCandidateColumn renders the candidate list, compacted to width.
func (m *appModel) renderCandidateColumn(width, height int) string {
	if width <= 0 {
		return ""
	}
	if len(m.candidates) == 0 {
		return dimStyle.Render("All clear — nothing needs a decision.")
	}
	// Column header + rule, only when there's room.
	headerLines := 0
	var b strings.Builder
	if width >= 56 {
		b.WriteString(sectStyle.Render(padTo("TRIAGE", width)) + "\n")
		b.WriteString(ruleStyle.Render(strings.Repeat("─", width)) + "\n")
		headerLines = 2
	}
	dataH := height - headerLines
	if dataH < 1 {
		dataH = 1
	}

	start := windowStart(m.cursor, len(m.candidates), dataH)
	end := start + dataH
	if end > len(m.candidates) {
		end = len(m.candidates)
	}
	for i := start; i < end; i++ {
		c := m.candidates[i]
		sel := i == m.cursor
		line := m.candidateCompactRow(c, width, sel)
		if i < end-1 {
			line += "\n"
		}
		b.WriteString(line)
	}
	return b.String()
}

// candidateCompactRow renders one candidate row. At full width it shows the full
// set of columns (action, sender, subject, inbox, age, cohort); sidebars collapse.
func (m *appModel) candidateCompactRow(c *triage.Candidate, width int, sel bool) string {
	badge := actionBadge[c.Pred.Action]
	if badge == "" {
		badge = "?"
	}
	sender := c.Thread.FromEmail
	if sender == "" {
		sender = c.Thread.FromName
	}
	subj := c.Thread.Subject

	// Slots sum exactly to width: 2 (cursor frame) + badge + gaps + sender + subj
	// (+ inbox + age + cohort at full width).
	var line string
	switch {
	case width >= 84:
		badgeW, senderW, acctW, ageW, cohortW := 9, 22, 4, 7, 6
		subjW := width - 60
		if subjW < 6 {
			subjW = 6
		}
		badgeCell := actionColor[c.Pred.Action].Render(fit(badge, badgeW))
		acctCell := accountStyle[c.Thread.Account].Render(fit(accountTag(c.Thread.Account), acctW))
		ageCell := dimStyle.Render(fit(ageString(c.Thread.Date, m.now), ageW))
		cohortCell := dimStyle.Render(fit(fmt.Sprintf("×%d", c.CohortCount()), cohortW))
		line = "  " + badgeCell + "  " + fit(sender, senderW) + "  " + fit(subj, subjW) +
			"  " + acctCell + "  " + ageCell + "  " + cohortCell
	case width >= 48:
		badgeCell := actionColor[c.Pred.Action].Render(fit(badge, 8))
		line = "  " + badgeCell + " " + fit(sender, 22) + " " + fit(subj, width-34)
	default:
		line = "  " + fit(sender, width-13) + " " + fit(subj, 10)
	}
	if sel {
		line = "▸" + line[1:]
		line = selStyle.Render(line)
	} else {
		line = " " + line[1:]
	}
	return fitAnsi(line, width)
}

// renderThreadColumn renders the selected candidate's cohort threads.
func (m *appModel) renderThreadColumn(width, height int) string {
	if width <= 0 {
		return ""
	}
	if m.level >= levelThreads && len(m.threads) == 0 {
		return dimStyle.Render("No other threads from this sender.")
	}
	headerLines := 0
	var b strings.Builder
	if width >= 40 {
		b.WriteString(sectStyle.Render(padTo("THREADS", width)) + "\n")
		b.WriteString(ruleStyle.Render(strings.Repeat("─", width)) + "\n")
		headerLines = 2
	}
	dataH := height - headerLines
	if dataH < 1 {
		dataH = 1
	}
	start := windowStart(m.selThread, len(m.threads), dataH)
	end := start + dataH
	if end > len(m.threads) {
		end = len(m.threads)
	}
	for i := start; i < end; i++ {
		sel := i == m.selThread
		line := m.threadCompactRow(m.threads[i], width, sel)
		if i < end-1 {
			line += "\n"
		}
		b.WriteString(line)
	}
	return b.String()
}

func (m *appModel) threadCompactRow(t *model.Thread, width int, sel bool) string {
	sender := t.FromEmail
	if sender == "" {
		sender = t.FromName
	}
	var line string
	if width >= 60 {
		meta := fmt.Sprintf("%s · %d msg", ageString(t.Date, m.now), t.MessageCount)
		metaW := visWidth(meta)
		metaCell := dimStyle.Render(fit(meta, metaW))
		subjW := width - 28 - metaW
		if subjW < 4 {
			subjW = 4
		}
		line = "  " + fit(sender, 24) + " " + fit(t.Subject, subjW) + " " + metaCell
	} else {
		subjW := width - 19
		if subjW < 3 {
			subjW = 3
		}
		line = "  " + fit(sender, 16) + " " + fit(t.Subject, subjW)
	}
	if sel {
		line = "▸" + line[1:]
		line = selStyle.Render(line)
	} else {
		line = " " + line[1:]
	}
	return fitAnsi(line, width)
}

// renderMessageColumn renders the selected thread's messages + plaintext contents.
func (m *appModel) renderMessageColumn(width, height int) string {
	if width <= 0 {
		return ""
	}
	if m.messagesLoading {
		return dimStyle.Render("Loading messages…")
	}
	if m.messagesErr != "" {
		return errStyle.Render("⚠ " + m.messagesErr)
	}
	if len(m.messages) == 0 {
		return dimStyle.Render("No message content.")
	}
	m.msgWidth = width
	lines, starts := m.buildMessageContent(width)
	m.msgLines, m.msgStarts = lines, starts
	// Clamp scroll to the visible window.
	maxScroll := len(lines) - height
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.msgScroll > maxScroll {
		m.msgScroll = maxScroll
	}
	if m.msgScroll < 0 {
		m.msgScroll = 0
	}
	end := m.msgScroll + height
	if end > len(lines) {
		end = len(lines)
	}
	view := lines[m.msgScroll:end]
	return strings.Join(view, "\n")
}

// buildMessageContent wraps each message (header + body) to width and records
// the starting line of every message so j/k can scroll to it.
func (m *appModel) buildMessageContent(width int) ([]string, []int) {
	bodyW := width - 2
	if bodyW < 10 {
		bodyW = 10
	}
	var lines []string
	var starts []int
	for i, msg := range m.messages {
		starts = append(starts, len(lines))
		// Header line (reserve 2 for the cursor marker).
		from := msg.FromName
		if from == "" {
			from = msg.FromEmail
		}
		date := ageString(msg.Date, m.now)
		subjW := width - 36
		if subjW < 10 {
			subjW = 10
		}
		content := fmt.Sprintf("%s · %s · %s", fit(from, 24), fit(msg.Subject, subjW), date)
		content = fit(content, width-2)
		var hl string
		if i == m.selMsg {
			hl = selStyle.Render("▸ " + content)
		} else {
			hl = headerStyle.Render("  " + content)
		}
		lines = append(lines, hl)
		// Body wrapped.
		body := m.wrapBody(msg.BodyText, bodyW)
		if body == "" {
			body = dimStyle.Render("(" + msg.Snippet + ")")
		}
		for _, bl := range strings.Split(body, "\n") {
			lines = append(lines, "  "+fit(bl, bodyW))
		}
		// Separator.
		lines = append(lines, dimStyle.Render(strings.Repeat("─", width)))
	}
	return lines, starts
}

// wrapBody word-wraps a plain-text body to a display width (ANSI-aware).
func (m *appModel) wrapBody(body string, width int) string {
	if body == "" {
		return ""
	}
	// Preserve intentional paragraph breaks, wrapping each paragraph.
	paras := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n\n")
	var out []string
	for _, p := range paras {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, ansi.Wrap(p, width, " \t"))
	}
	return strings.Join(out, "\n\n")
}

// windowStart computes the first visible row so the cursor stays centred.
func windowStart(cursor, total, size int) int {
	start := cursor - size/2
	if start < 0 {
		start = 0
	}
	if start+size > total {
		start = total - size
	}
	if start < 0 {
		start = 0
	}
	return start
}

func clamp(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

// visWidth is the display width of s, stripping ANSI escape codes and using
// East-Asian-aware runewidth so wide glyphs (emoji/CJK) match how the terminal
// renders them.
func visWidth(s string) int {
	return runewidth.StringWidth(ansi.Strip(s))
}

// lineWidth returns the widest line in s (by display width).
func lineWidth(s string) int {
	w := 0
	for _, l := range strings.Split(s, "\n") {
		if dw := visWidth(l); dw > w {
			w = dw
		}
	}
	return w
}

// fitAnsi pads s to display width w, honouring ANSI and wide (2-cell) glyphs.
func fitAnsi(s string, w int) string {
	if w <= 0 {
		return ""
	}
	tw := visWidth(s)
	if tw < w {
		return s + strings.Repeat(" ", w-tw)
	}
	return s
}

// enterLevel1 drills from the candidate list into the selected candidate's
// sender-cohort threads. The cohort already includes that candidate's thread.
func (m *appModel) enterLevel1() {
	if m.level != levelList || len(m.candidates) == 0 {
		return
	}
	can := m.candidates[m.cursor]
	m.threads = can.Cohort
	if len(m.threads) == 0 {
		m.threads = []*model.Thread{can.Thread}
	}
	m.selThread = indexThread(m.threads, can.Thread.ID)
	if m.selThread < 0 {
		m.selThread = 0
	}
	m.level = levelThreads
	m.messages = nil
	m.messagesErr = ""
}

// enterLevel2 drills into the selected thread's messages (fetched async).
func (m *appModel) enterLevel2() tea.Cmd {
	if m.level != levelThreads || len(m.threads) == 0 {
		return nil
	}
	th := m.threads[m.selThread]
	m.level = levelMessages
	m.messages = nil
	m.messagesErr = ""
	m.messagesLoading = true
	m.selMsg = 0
	m.msgScroll = 0
	return m.fetchMessages(th)
}

// fetchMessages loads a thread's messages off the UI loop.
func (m *appModel) fetchMessages(th *model.Thread) tea.Cmd {
	return func() tea.Msg {
		if m.engine == nil {
			return messagesMsg{threadID: th.ID, err: fmt.Errorf("no engine")}
		}
		msgs, err := m.engine.Messages(m.ctx, th)
		return messagesMsg{threadID: th.ID, msgs: msgs, err: err}
	}
}

// onMessages stores fetched messages and re-enables the message pane.
func (m *appModel) onMessages(msg messagesMsg) tea.Cmd {
	if m.level != levelMessages || len(m.threads) == 0 || msg.threadID != m.threads[m.selThread].ID {
		return m.waitProgress()
	}
	m.messagesLoading = false
	m.messagesErr = ""
	if msg.err != nil {
		m.messagesErr = msg.err.Error()
		m.messages = nil
	} else {
		m.messages = msg.msgs
		m.selMsg = 0
		m.msgScroll = 0
		m.msgStarts = nil
	}
	return m.waitProgress()
}

// goUp steps back one drilldown depth.
func (m *appModel) goUp() {
	switch m.level {
	case levelMessages:
		m.level = levelThreads
		m.messages = nil
		m.messagesErr = ""
		m.messagesLoading = false
		m.msgScroll = 0
	case levelThreads:
		m.level = levelList
		m.threads = nil
		m.selThread = 0
	}
}

// drillRight advances the drilldown one level.
func (m *appModel) drillRight() (tea.Model, tea.Cmd) {
	switch m.level {
	case levelList:
		m.enterLevel1()
	case levelThreads:
		return m, m.enterLevel2()
	}
	return m, m.waitProgress()
}

// drillLeft returns to the previous level (or closes a detail window).
func (m *appModel) drillLeft() (tea.Model, tea.Cmd) {
	if m.detail != nil {
		m.detail = nil
		return m, m.waitProgress()
	}
	m.goUp()
	return m, m.waitProgress()
}

// moveCursor moves within the currently focused column.
func (m *appModel) moveCursor(delta int) {
	switch m.level {
	case levelList:
		m.move(delta)
	case levelThreads:
		m.moveThread(delta)
	case levelMessages:
		m.moveMsg(delta)
	}
}

func (m *appModel) moveThread(delta int) {
	if len(m.threads) == 0 {
		m.selThread = 0
		return
	}
	m.selThread = clamp(m.selThread+delta, 0, len(m.threads)-1)
}

func (m *appModel) moveMsg(delta int) {
	if len(m.messages) == 0 {
		m.selMsg = 0
		return
	}
	m.selMsg = clamp(m.selMsg+delta, 0, len(m.messages)-1)
	if m.msgWidth > 0 {
		m.msgStarts = m.computeMessageStarts(m.msgWidth)
	}
	if m.selMsg < len(m.msgStarts) {
		m.msgScroll = m.msgStarts[m.selMsg]
	}
}

// jumpCursor moves the focused column cursor to the top (pos==0) or bottom.
func (m *appModel) jumpCursor(pos int) {
	switch m.level {
	case levelList:
		if pos == 0 {
			m.cursor = 0
		} else {
			m.cursor = len(m.candidates) - 1
		}
	case levelThreads:
		if pos == 0 {
			m.selThread = 0
		} else {
			m.selThread = len(m.threads) - 1
		}
	case levelMessages:
		if pos == 0 {
			m.selMsg = 0
		} else {
			m.selMsg = len(m.messages) - 1
		}
		m.msgScroll = 0
	}
}

// updateMouse handles wheel/click scrolling for the focused column.
func (m *appModel) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if !m.loaded {
		return m, m.idleCmd()
	}
	switch msg.Action {
	case tea.MouseActionPress:
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			m.mouseScroll(-1)
		case tea.MouseButtonWheelDown:
			m.mouseScroll(1)
		}
	}
	return m, m.waitProgress()
}

func (m *appModel) mouseScroll(dir int) {
	switch m.level {
	case levelList:
		m.move(dir)
	case levelThreads:
		m.moveThread(dir)
	case levelMessages:
		m.msgScroll += dir * 3
	}
}

// indexThread returns the index of a thread id, or -1.
func indexThread(ts []*model.Thread, id string) int {
	for i, t := range ts {
		if t.ID == id {
			return i
		}
	}
	return -1
}

// computeMessageStarts wraps the current messages and returns each message's
// starting line, using a previously-known pane width.
func (m *appModel) computeMessageStarts(width int) []int {
	_, starts := m.buildMessageContent(width)
	return starts
}
