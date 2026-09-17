package assistant

import (
	"time"
)

// Waiting for the field to answer.
//
// Two things the console does by polling -- an enrolment a customer is
// standing at, a command a terminal has been sent -- the assistant can wait
// on too, and both go through here. A wait is BOUNDED THREE WAYS: by the
// tool's own timeout (at most maxWaitSeconds), by the tool's MaxDuration,
// which the dispatcher enforces with a context, and by the turn's polling
// allowance (dispatch.go), which is separate from the ordinary call budget so
// that a minute spent watching a door costs the steps after it nothing.
//
// When the allowance is spent the wait does not fail: it reads the state once
// more, at the ordinary budget, and returns it with a note saying the waiting
// is over for this message. The model can still tell the operator where
// things stand.

const (
	// maxWaitSeconds is the longest one wait tool may block.
	maxWaitSeconds = 60
	// minPollInterval keeps a cadence from being tightened below what the
	// platform can learn anything new in.
	minPollInterval = time.Second

	pollBudgetNote = "The waiting allowance for this message is used up; this is the state now. " +
		"Send another message to keep waiting."
)

// pollUntil calls step at a fixed cadence until it reports done, fails, or
// the timeout passes, and returns the last outcome. The reads step makes
// are charged to the turn's polling allowance.
func pollUntil(t *Turn, timeout, every time.Duration, step func(p *Turn) (Outcome, bool)) Outcome {
	if timeout > maxWaitSeconds*time.Second {
		timeout = maxWaitSeconds * time.Second
	}
	if every < minPollInterval {
		every = minPollInterval
	}

	// The allowance was spent by an earlier wait in this turn: one ordinary
	// read, and an honest note.
	if t.polls >= maxPollCallsPerTurn {
		o, _ := step(t)
		return withNote(o, pollBudgetNote)
	}

	pt := *t
	pt.polling = true
	defer func() { t.polls = pt.polls }()

	deadline := time.Now().Add(timeout)
	for {
		o, done := step(&pt)
		if done || o.IsError || !time.Now().Before(deadline) {
			return o
		}
		if pt.polls >= maxPollCallsPerTurn {
			return withNote(o, pollBudgetNote)
		}
		select {
		case <-t.ctx.Done():
			return o
		case <-time.After(every):
		}
	}
}

// withNote adds a note to a successful object result.
func withNote(o Outcome, note string) Outcome {
	if o.IsError {
		return o
	}
	if m, ok := o.Result.(object); ok {
		out := object{}
		for k, v := range m {
			out[k] = v
		}
		out["note"] = note
		o.Result = out
	}
	return o
}
