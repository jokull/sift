package model

// Category is the coarse triage classification the AI (or fallback heuristics)
// assigns to a thread.
type Category string

const (
	CategoryKeep          Category = "keep"          // personal/meaningful — leave in inbox
	CategoryReceipt       Category = "receipt"       // purchase/invoice/order — auto-pluck to Receipts
	CategoryNewsletter    Category = "newsletter"    // subscription updates — auto-pluck to Reading
	CategoryPromotion     Category = "promotion"     // marketing/promo — decision per sender
	CategoryTransactional Category = "transactional" // account/notification — decision per sender
	CategoryActionable    Category = "actionable"    // needs eyes (Sentry/errors) — bulk-archive after 24h
	CategoryUnknown       Category = "unknown"
)

// Action is what sift will actually do to a thread (given a folder mapping).
type Action string

const (
	ActionKeep        Action = "keep"        // do nothing; stays in inbox
	ActionArchive     Action = "archive"     // remove from inbox (cross-account archive)
	ActionReceipts    Action = "receipts"    // move to Receipts folder
	ActionReading     Action = "reading"     // move to Reading folder
	ActionUnsubscribe Action = "unsubscribe" // archive AND mark sender for unsubscribe
	ActionSpam        Action = "spam"        // mark as spam (move to Spam/Junk)
	ActionDelete      Action = "delete"      // move to Trash (recoverable)
)

// Minor action label used for promoting a thread to "keep" (personal).
const (
	ActionFlagKeep Action = "keep"
)

// Prediction is the AI's per-thread verdict.
type Prediction struct {
	Category   Category
	Action     Action
	Confidence float64 // 0..1
	Reason     string  // short human-readable rationale
	SenderWide bool    // true if the action applies to every thread from this sender
}

// IsAuto reports whether the prediction's action is a no-decision automatic
// action (Receipts/Reading) versus one that should surface for per-sender
// confirmation (Archive/Unsubscribe/Keep).
func (p Prediction) IsAuto() bool {
	return p.Action == ActionReceipts || p.Action == ActionReading
}

// UnsubscribeInfo carries the parsed List-Unsubscribe header(s) from a message,
// used to actually unsubscribe from a mailing list (RFC 8058 one-click where
// available).
type UnsubscribeInfo struct {
	URLs     []string // candidate unsubscribe URLs (https)
	Mailto   string   // mailto address from List-Unsubscribe, if any
	OneClick bool     // List-Unsubscribe-Post: List-Unsubscribe=One-Click
	Raw      string   // the raw value of the header
}
