package triage

import (
	"strings"

	"github.com/jokull/sift/internal/model"
)

// heuristic is a deterministic fallback used when the classifier is unavailable
// or returns no verdict for a thread. It is intentionally conservative: it only
// triggers obvious non-personal patterns and defaults to keeping everything else.
func heuristic(t *model.Thread) model.Prediction {
	if t == nil {
		return model.Prediction{Category: model.CategoryUnknown, Action: model.ActionKeep}
	}
	subj := strings.ToLower(t.Subject)
	sender := strings.ToLower(t.SenderKey())

	p := model.Prediction{Confidence: 0.5, SenderWide: true}

	switch {
	case containsAny(subj, "receipt", "invoice", "order confirmation", "your order", "payment confirm", "booking confirmed", "ticket", "your bill"):
		p.Category, p.Action, p.Reason = model.CategoryReceipt, model.ActionReceipts, "looks like a receipt/invoice"
	case containsAny(sender, "sentry", "@sentry", "error", "alert@", "ci@", "build@", "failure"):
		p.Category, p.Action, p.Reason = model.CategoryActionable, model.ActionKeep, "possible error/alert"
	case containsAny(subj, "newsletter", "digest", "unsubscribe", "weekly roundup", "new articles"):
		p.Category, p.Action, p.Reason = model.CategoryNewsletter, model.ActionReading, "newsletter-like"
	case containsAny(subj, "unsubscribe", "limited time", "save", "sale", "offer", "get started", "% off"):
		p.Category, p.Action, p.Reason = model.CategoryPromotion, model.ActionArchive, "promotional keywords"
	case containsAny(subj, "your code", "verification", "password reset", "sign in", "login", "security alert", "confirm your"):
		p.Category, p.Action, p.Reason = model.CategoryTransactional, model.ActionArchive, "account/notification"
	default:
		p.Category, p.Action, p.Reason = model.CategoryKeep, model.ActionKeep, "no strong signal"
		p.SenderWide = false
	}
	return p
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}
