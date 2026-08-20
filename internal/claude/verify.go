package claude

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/your-company/agentic-code-review/internal/contextselect"
	"github.com/your-company/agentic-code-review/internal/findings"
	"github.com/your-company/agentic-code-review/internal/verification"
)

// MaxVerifierConcurrency bounds how many verifications run at once.
//
// Each one is a separate Claude Code process, so this is a real resource limit rather
// than a stylistic one: twenty concurrent invocations would compete for the same CPU and
// the same rate limit, and finish no sooner than three would.
const MaxVerifierConcurrency = 3

// Verifier runs the adversarial second opinion over a set of candidate findings.
//
// It owns one journey per finding: a targeted context is built, the verifier is invoked,
// and its answer is decoded strictly and validated against the finding it was asked
// about. It has no publishing authority and makes no GitHub call of any kind — it decides
// only what it believes, and the verification policy decides what that means.
type Verifier struct {
	client         *Client
	builder        VerifierInputBuilder
	contextBuilder *verification.ContextBuilder
	validator      verification.Validator

	// concurrency bounds simultaneous invocations. Zero means MaxVerifierConcurrency.
	concurrency int
}

// NewVerifier returns a Verifier using client for execution. A nil client means the
// default one.
func NewVerifier(client *Client) *Verifier {
	if client == nil {
		client = NewClient()
	}

	return &Verifier{
		client:         client,
		builder:        NewVerifierInputBuilder(),
		contextBuilder: verification.NewContextBuilder(),
		validator:      verification.NewValidator(),
		concurrency:    MaxVerifierConcurrency,
	}
}

// WithConcurrency returns a copy of the verifier with an explicit concurrency bound. A
// value below one falls back to serial execution.
func (v *Verifier) WithConcurrency(n int) *Verifier {
	copied := *v
	if n < 1 {
		n = 1
	}
	copied.concurrency = n
	return &copied
}

// Available reports whether the Claude Code executable can be found.
func (v *Verifier) Available(ctx context.Context) error { return v.client.Available(ctx) }

// VerifyAll verifies every candidate finding that policy calls for.
//
// Findings that cannot reach an inline comment are not sent: they are recorded as not
// required, which is neither a pass nor a suppression. Everything else gets one
// invocation, bounded and independent, and one failure never contaminates another
// finding's result. The returned outcomes are in the same order as the input findings,
// whatever order the concurrent invocations completed in.
//
// An error is returned only when the caller's context ended. A verification that fails on
// its own — a timeout, malformed output, a rejected verdict — is recorded as a failed
// outcome, which the policy treats as fail-closed.
func (v *Verifier) VerifyAll(
	ctx context.Context,
	result findings.ReviewResult,
	selected contextselect.SelectedContext,
	repoDir string,
) (verification.Report, error) {
	outcomes := make([]verification.Outcome, len(result.Findings))

	type job struct {
		index   int
		finding findings.Finding
	}
	var jobs []job

	// Decide first, invoke second. Everything skipped is settled before a single
	// process starts, so the cost of the stage is knowable from the findings alone.
	for i, finding := range result.Findings {
		if !verification.RequiresVerification(finding) {
			outcomes[i] = verification.Outcome{
				Finding:    finding,
				Status:     verification.StatusNotRequired,
				SkipReason: verification.ReasonNotRequiredLowSeverity,
			}
			continue
		}
		jobs = append(jobs, job{index: i, finding: finding})
	}

	if len(jobs) > 0 {
		concurrency := v.concurrency
		if concurrency < 1 {
			concurrency = 1
		}
		if concurrency > len(jobs) {
			concurrency = len(jobs)
		}

		queue := make(chan job)
		var wg sync.WaitGroup

		// Each worker writes only to its own index, so no lock is needed around the
		// results and the ordering is the input's, not the completion order's.
		for w := 0; w < concurrency; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := range queue {
					outcomes[j.index] = v.verifyOne(ctx, j.finding, selected, repoDir)
				}
			}()
		}

	dispatch:
		for _, j := range jobs {
			select {
			case queue <- j:
			case <-ctx.Done():
				break dispatch
			}
		}
		close(queue)
		wg.Wait()
	}

	// A cancelled parent context is the caller's decision, not a verification outcome.
	if err := ctx.Err(); err != nil {
		return verification.Report{}, fmt.Errorf("verification interrupted: %w", err)
	}

	// Any finding never dispatched — only possible if the context ended mid-dispatch —
	// is recorded as failed rather than left zero, so nothing unverified looks verified.
	for i := range outcomes {
		if outcomes[i].Status == "" {
			outcomes[i] = verification.Outcome{
				Finding:       result.Findings[i],
				Status:        verification.StatusFailed,
				FailureReason: "verification was not attempted",
			}
		}
	}

	return verification.Report{Outcomes: outcomes, Stats: tally(outcomes)}, nil
}

// verifyOne builds the context for a finding, invokes the verifier, and validates the
// answer. It never returns an error: a failure is the finding's outcome.
func (v *Verifier) verifyOne(
	ctx context.Context,
	finding findings.Finding,
	selected contextselect.SelectedContext,
	repoDir string,
) verification.Outcome {
	outcome := verification.Outcome{Finding: finding}

	vctx, err := v.contextBuilder.Build(finding, selected)
	outcome.ContextBytes = vctx.Bytes

	// A missing patch is not fatal. The verifier is told the diff is unavailable and is
	// expected to answer uncertain, which is exactly the honest outcome.
	if err != nil && !errors.Is(err, verification.ErrNoPatch) {
		outcome.Status = verification.StatusFailed
		outcome.FailureReason = fmt.Sprintf("build verification context: %v", err)
		return outcome
	}

	input, err := v.builder.Build(vctx)
	if err != nil {
		outcome.Status = verification.StatusFailed
		outcome.FailureReason = fmt.Sprintf("build verifier input: %v", err)
		return outcome
	}
	outcome.ContextBytes = input.Bytes()

	transport, err := v.client.Verify(ctx, Request{Input: input.Content, WorkingDirectory: repoDir})
	if err != nil {
		outcome.Status = verification.StatusFailed
		outcome.FailureReason = transportFailure(err)
		return outcome
	}

	result, err := verification.Decode(transport.Output)
	if err != nil {
		outcome.Status = verification.StatusFailed
		outcome.FailureReason = fmt.Sprintf("parse verification result: %v", err)
		return outcome
	}

	// The verdict must be about the finding it was asked about. A mismatch is refused,
	// not reconciled: attaching one claim's conclusion to another is worse than having
	// no verification at all.
	if err := v.validator.Validate(result, finding.ID); err != nil {
		outcome.Status = verification.StatusFailed
		outcome.FailureReason = fmt.Sprintf("validate verification result: %v", err)
		return outcome
	}

	outcome.Status = verification.StatusVerified
	outcome.Result = result
	return outcome
}

// transportFailure renders an invocation failure for reporting. The client already keeps
// credentials out of its errors; this only bounds the length.
func transportFailure(err error) string {
	const maxLen = 300

	message := err.Error()
	if len(message) > maxLen {
		message = message[:maxLen] + "…"
	}
	return message
}

// tally counts the outcomes.
func tally(outcomes []verification.Outcome) verification.Stats {
	stats := verification.Stats{Candidates: len(outcomes)}

	for _, outcome := range outcomes {
		stats.ContextBytes += outcome.ContextBytes

		switch outcome.Status {
		case verification.StatusNotRequired:
			stats.Skipped++
		case verification.StatusFailed:
			stats.Failed++
		case verification.StatusVerified:
			stats.Verified++
			switch outcome.Result.Verdict {
			case verification.VerdictValid:
				stats.Valid++
			case verification.VerdictInvalid:
				stats.Invalid++
			case verification.VerdictUncertain:
				stats.Uncertain++
			}
		}
	}

	return stats
}
