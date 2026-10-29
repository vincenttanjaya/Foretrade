// Package researchrun is the neutral boundary between the automation owner
// (admission, authority, accounting and publication) and its read-only runner.
package researchrun

import (
	"context"
	"github.com/vincenttanjaya/Foretrade/api/internal/profile"
	"time"
)

const Cost = 2
const Deadline = 120 * time.Second
const Lease = 130 * time.Second

type Thesis struct {
	Statement    string `json:"statement"`
	Invalidation string `json:"invalidation"`
}

type Source struct {
	N           int        `json:"n"`
	Title       string     `json:"title"`
	URL         string     `json:"url"`
	Source      string     `json:"source"`
	PublishedAt *time.Time `json:"publishedAt"`
	Kind        string     `json:"kind"`
}

type Request struct {
	UserID, AlertID, RunID string
	Symbol, Prompt         string
	Thesis                 *Thesis
	Profile                profile.Profile // captured by the claim transaction; never client supplied
	Previous               string
	PreviousAt             *time.Time
	AdmittedAt, Deadline   time.Time // one local monotonic clock domain; includes pool/lock waits and publication
	// BeforeModel must authorize and durably mark an attempt before the SDK.
	// Any error, including an ambiguous marker commit, forbids dispatch.
	BeforeModel func(context.Context) error
	// BeforeTool authorizes before every admitted call, including unknown names.
	BeforeTool func(context.Context) error
}

type Result struct {
	Answer   string
	Sources  []Source
	Produced bool // ANY raw SDK event, including private/malformed/suppressed events
	// Settled proves every dispatched request has a definitive outcome. It is
	// false for timeout, transport loss, cancellation or ambiguous production.
	// An error refunds after an attempt only when Settled && !Produced.
	Settled bool
}

type Runner interface {
	Run(context.Context, Request) (Result, error)
}
