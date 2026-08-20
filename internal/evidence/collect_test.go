package evidence

import (
	"context"
	"errors"
	"testing"
)

type fakeConnector struct {
	id       string
	required bool
	document Document
	err      error
}

func (f fakeConnector) ID() string       { return f.id }
func (f fakeConnector) Type() SourceType { return SourceFile }
func (f fakeConnector) Kind() Kind       { return KindRequirement }
func (f fakeConnector) Required() bool   { return f.required }
func (f fakeConnector) Collect(context.Context) (Document, error) {
	return f.document, f.err
}

func TestCollectorKeepsOrderAndReportsOptionalFailure(t *testing.T) {
	report, err := NewCollector().Collect(context.Background(), []Connector{
		fakeConnector{id: "first", document: Document{ID: "first", Content: "one"}},
		fakeConnector{id: "optional", err: errors.New("not reachable\ntry later")},
		fakeConnector{id: "third", document: Document{ID: "third", Content: "three"}},
	})
	if err != nil {
		t.Fatalf("Collect() = %v", err)
	}
	if len(report.Documents) != 2 || report.Documents[0].ID != "first" || report.Documents[1].ID != "third" {
		t.Fatalf("documents = %+v", report.Documents)
	}
	if got := report.Outcomes[1]; got.Status != StatusFailed || got.Error != "not reachable try later" {
		t.Fatalf("optional outcome = %+v", got)
	}
}

func TestCollectorFailsClosedForRequiredSource(t *testing.T) {
	report, err := NewCollector().Collect(context.Background(), []Connector{
		fakeConnector{id: "required", required: true, err: errors.New("permission denied")},
	})
	if !errors.Is(err, ErrRequiredSource) {
		t.Fatalf("Collect() = %v, want ErrRequiredSource", err)
	}
	if len(report.Outcomes) != 1 || report.Outcomes[0].Status != StatusFailed {
		t.Fatalf("report = %+v", report)
	}
}
