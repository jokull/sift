package ai

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jokull/sift/internal/model"
)

const classifySystem = `You are an email triage classifier for a personal inbox. You receive a JSON list of threads (id, sender, subject, optional hint) and must return ONLY a JSON object: {"items":[{"id":"<thread id>","category":"...","action":"...","confidence":0.0,"reason":"<=12 words","sender_wide":true}]}. No prose, no markdown, no reasoning.

Category must be exactly one of: keep, receipt, newsletter, promotion, transactional, actionable.
Action must be exactly one of: keep, archive, receipts, reading, unsubscribe.

Rules:
- keep: personal, from a real person, or clearly needs the owner's eyes (meaningful conversation, decisions, direct asks). sender_wide false.
- receipt: purchase/invoice/order/booking confirmation or payment receipt. Action receipts. sender_wide true.
- newsletter: mailing list, blog digest, product updates, announcements, Substack, Figma, community emails. Action reading. sender_wide true.
- promotion: marketing, sales offers, product launches, discounts. Default Action archive (or unsubscribe if it looks like spammy bulk marketing). sender_wide true.
- transactional: account/notification/security/billing-usage alerts that don't need a decision (password resets, login codes, ad/account notices, billing status). Default Action archive. sender_wide true.
- actionable: error/exception/alert that may need evaluation (Sentry, deploy failures, CI, review requests). Action keep so the owner reviews it. sender_wide false.

Use the hint (Gmail category or known sender type) as a strong prior but still judge subject. Keep confidence high (>=0.7) when sender+subject are unambiguous; lower otherwise.`

// classifyItem is one input row.
type classifyItem struct {
	ID      string `json:"id"`
	Sender  string `json:"sender"`
	Subject string `json:"subject"`
	Hint    string `json:"hint,omitempty"`
	Snippet string `json:"snippet,omitempty"`
}

// classifyReply is the model's per-item output.
type classifyReply struct {
	Items []struct {
		ID         string  `json:"id"`
		Category   string  `json:"category"`
		Action     string  `json:"action"`
		Confidence float64 `json:"confidence"`
		Reason     string  `json:"reason"`
		SenderWide bool    `json:"sender_wide"`
	} `json:"items"`
}

// ClassifyThreads classifies a batch of threads in one DeepSeek call and returns
// a prediction keyed by thread ID. Order is preserved by id mapping.
func (c *Client) ClassifyThreads(ctx context.Context, threads []*model.Thread) (map[string]model.Prediction, error) {
	result := map[string]model.Prediction{}
	if len(threads) == 0 {
		return result, nil
	}

	items := make([]classifyItem, 0, len(threads))
	for _, t := range threads {
		items = append(items, classifyItem{
			ID:      t.ID,
			Sender:  t.SenderKey(),
			Subject: t.Subject,
			Hint:    hintFor(t),
			Snippet: truncate(t.Snippet, 140),
		})
	}
	payload, err := json.Marshal(map[string]any{"threads": items})
	if err != nil {
		return nil, err
	}
	user := "Classify these threads. threads=" + string(payload)

	var reply classifyReply
	if err := c.CompleteJSON(ctx, classifySystem, user, &reply); err != nil {
		return nil, err
	}
	for _, item := range reply.Items {
		p := model.Prediction{
			Category:   categoryOf(item.Category),
			Action:     actionOf(item.Action),
			Confidence: item.Confidence,
			Reason:     strings.TrimSpace(item.Reason),
			SenderWide: item.SenderWide,
		}
		result[item.ID] = p
	}
	return result, nil
}

func categoryOf(s string) model.Category {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "keep", "personal", "human", "important":
		return model.CategoryKeep
	case "receipt", "receipts", "invoice", "payment":
		return model.CategoryReceipt
	case "newsletter", "digest", "updates", "blog":
		return model.CategoryNewsletter
	case "promotion", "promo", "marketing", "ads", "offer":
		return model.CategoryPromotion
	case "transactional", "notification", "system", "account", "billing", "verify":
		return model.CategoryTransactional
	case "actionable", "alert", "error", "review", "todo":
		return model.CategoryActionable
	}
	return model.CategoryUnknown
}

func actionOf(s string) model.Action {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "keep", "leave", "inbox", "personal":
		return model.ActionKeep
	case "archive", "trash", "remove":
		return model.ActionArchive
	case "receipts", "receipt":
		return model.ActionReceipts
	case "reading", "read":
		return model.ActionReading
	case "unsubscribe", "opt_out":
		return model.ActionUnsubscribe
	}
	return model.ActionKeep
}

// hintFor derives a strong prior from Gmail's built-in categories so the model
// does less work.
func hintFor(t *model.Thread) string {
	if t.Account != model.AccountGmail {
		return ""
	}
	for _, l := range t.Labels {
		switch l {
		case "CATEGORY_PROMOTIONS":
			return "gmail:promotions"
		case "CATEGORY_UPDATES":
			return "gmail:updates"
		case "CATEGORY_SOCIAL":
			return "gmail:social"
		case "CATEGORY_FORUMS":
			return "gmail:forums"
		case "CATEGORY_PERSONAL":
			return "gmail:personal"
		}
	}
	return ""
}
