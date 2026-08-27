package ui

import (
	"fmt"
	"strings"

	"github.com/jokull/sift/internal/model"
	"github.com/jokull/sift/internal/triage"
)

type detailModel struct {
	can *triage.Candidate
}

func newDetail(can *triage.Candidate) *detailModel { return &detailModel{can: can} }

func (d *detailModel) defaultAction() model.Action {
	return d.can.Pred.Action
}

// render returns the decision window string for the selected thread.
func (d *detailModel) render() string {
	can := d.can
	t := can.Thread
	conf := int(can.Pred.Confidence * 100)
	lines := []string{}

	lines = append(lines, headerStyle.Render("── decision ──"))
	lines = append(lines, titleStyle.Render(truncateRunewidth(t.Subject, 60)))
	lines = append(lines, fmt.Sprintf("from:     %s", accountStyle[t.Account].Render(accountTag(t.Account))+" "+t.FromName+" <"+t.FromEmail+">"))
	lines = append(lines, fmt.Sprintf("cats:     %s", clusterCats(can.Cohort)))
	if can.CohortCount() > 0 {
		lines = append(lines, fmt.Sprintf("cohort:   %d threads from this sender", can.CohortCount()+1))
		lines = append(lines, "\n"+warnStyle.Render(fmt.Sprintf("  %d other thread(s) from %s would be affected by the same choice.",
			can.CohortCount(), t.SenderKey())))
	}
	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf("AI calls it: %s → %s  (conf %d%%)", catName(can.Pred.Category), actionName(can.Pred.Action), conf))
	if can.Pred.Reason != "" {
		lines = append(lines, fmt.Sprintf("  reason:    %s", can.Pred.Reason))
	}
	lines = append(lines, "")
	lines = append(lines, accentStyle.Render("[a] archive  [u] unsubscribe  [r] receipts  [n] reading  [s] keep  [x] apply default to whole sender  [b] back"))

	return strings.Join(lines, "\n")
}

func catName(c model.Category) string {
	switch c {
	case model.CategoryKeep:
		return "keep"
	case model.CategoryReceipt:
		return "receipt"
	case model.CategoryNewsletter:
		return "newsletter"
	case model.CategoryPromotion:
		return "promotion"
	case model.CategoryTransactional:
		return "transactional"
	case model.CategoryActionable:
		return "actionable"
	}
	return "unknown"
}

// clusterCats summarises the category mix across a cohort.
func clusterCats(cohort []*model.Thread) string {
	if len(cohort) == 0 {
		return "1 thread"
	}
	byAccount := map[model.Account]int{}
	for _, t := range cohort {
		byAccount[t.Account]++
	}
	parts := []string{}
	if n := byAccount[model.AccountFastmail]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d[f]", n))
	}
	if n := byAccount[model.AccountGmail]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d[g]", n))
	}
	return strings.Join(parts, " ")
}
