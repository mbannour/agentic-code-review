package publish

import (
	"context"
	"testing"

	"github.com/your-company/agentic-code-review/internal/findings"
	"github.com/your-company/agentic-code-review/internal/verification"
)

// allSuppressedPlan is a plan where policy withheld everything: findings exist, and none of
// them may be published.
func allSuppressedPlan() Plan {
	f := codeFinding("COR-001", findings.CategoryCorrectness, findings.SeverityHigh, 0.82)
	return NewPolicy().BuildPlan(
		[]Candidate{verifiedCandidate(f, verification.VerdictValid, 0.72)},
		mapperFixture(), testHeadSHA, "s")
}

// TestAllSuppressedPublishesNothing checks a review is not created when policy suppressed
// every finding.
//
// This is distinct from finding nothing. The plan accounts for real findings, so it is not
// empty — but none of them reaches GitHub, and a review whose body reads "0 findings" is a
// notification with nothing in it. It trains people to ignore the next one.
func TestAllSuppressedPublishesNothing(t *testing.T) {
	plan := allSuppressedPlan()
	if len(plan.Inline) != 0 || len(plan.Summary) != 0 || len(plan.Suppressed) != 1 {
		t.Fatalf("fixture is wrong: %d inline, %d summary, %d suppressed",
			len(plan.Inline), len(plan.Summary), len(plan.Suppressed))
	}

	api := &fakeAPI{currentSHA: testHeadSHA}
	outcome, err := NewPublisher(api).Publish(context.Background(), Request{
		PullRequest: testPullRequest, Plan: plan, ReviewedHeadSHA: testHeadSHA,
	})
	if err != nil {
		t.Fatalf("Publish() = %v", err)
	}

	if len(api.created) != 0 {
		t.Fatalf("published a review with nothing in it; status=%q body:\n%s",
			outcome.Status, api.created[0].Body)
	}
	if outcome.Status != StatusSkippedNoFindings {
		t.Errorf("Status = %q, want %q", outcome.Status, StatusSkippedNoFindings)
	}
	if api.getCalls != 0 || api.listCalls != 0 {
		t.Error("an unpublishable plan reached GitHub")
	}

	// A summary-only plan is the case that must still publish.
	summaryOnly := NewPolicy().BuildPlan(
		[]Candidate{verifiedCandidate(
			codeFinding("MAINT-001", findings.CategoryMaintainability, findings.SeverityLow, 0.90),
			verification.VerdictValid, 0)},
		mapperFixture(), testHeadSHA, "s")

	if summaryOnly.NothingToPublish() {
		t.Fatal("a summary-only plan reports nothing to publish")
	}

	api2 := &fakeAPI{currentSHA: testHeadSHA}
	if _, err := NewPublisher(api2).Publish(context.Background(), Request{
		PullRequest: testPullRequest, Plan: summaryOnly, ReviewedHeadSHA: testHeadSHA,
	}); err != nil {
		t.Fatalf("Publish() = %v", err)
	}
	if len(api2.created) != 1 {
		t.Errorf("created %d reviews, want 1 for a summary-only plan", len(api2.created))
	}
}
