// Package evidence gathers bounded, read-only context from operator-configured
// systems outside GitHub and Jira.
//
// Connectors never hand credentials, clients, or executable instructions to the
// reviewer. They return normalized documents; every downstream stage treats the
// document content as untrusted data.
package evidence

import (
	"context"
	"strings"
)

// Kind describes how a document may be used during review.
type Kind string

const (
	KindRequirement    Kind = "requirement"
	KindArchitecture   Kind = "architecture"
	KindDatabaseSchema Kind = "database_schema"
	KindReference      Kind = "reference"
)

// Kinds returns every supported kind in stable order.
func Kinds() []Kind {
	return []Kind{KindRequirement, KindArchitecture, KindDatabaseSchema, KindReference}
}

// Valid reports whether k is supported.
func (k Kind) Valid() bool {
	for _, allowed := range Kinds() {
		if k == allowed {
			return true
		}
	}
	return false
}

// SourceType identifies where a document was collected.
type SourceType string

const (
	SourceFile           SourceType = "file"
	SourceConfluence     SourceType = "confluence"
	SourcePostgresSchema SourceType = "postgres_schema"
)

// SourceTypes returns every supported connector type in stable order.
func SourceTypes() []SourceType {
	return []SourceType{SourceFile, SourceConfluence, SourcePostgresSchema}
}

// Valid reports whether t is supported.
func (t SourceType) Valid() bool {
	for _, allowed := range SourceTypes() {
		if t == allowed {
			return true
		}
	}
	return false
}

// Document is one normalized, bounded piece of external review evidence.
type Document struct {
	ID         string
	Kind       Kind
	SourceType SourceType
	Locator    string
	Title      string
	Revision   string
	Content    string
	Digest     string
	Truncated  bool
}

// Empty reports whether the document contains no useful text.
func (d Document) Empty() bool { return strings.TrimSpace(d.Content) == "" }

// Connector is the deliberately small capability exposed to collection. A
// connector may read one configured source and may do nothing else.
type Connector interface {
	ID() string
	Type() SourceType
	Kind() Kind
	Required() bool
	Collect(ctx context.Context) (Document, error)
}

// Status is the outcome of one connector attempt.
type Status string

const (
	StatusCollected Status = "collected"
	StatusSkipped   Status = "skipped"
	StatusFailed    Status = "failed"
)

// Outcome is an auditable connector result. Error contains a safe diagnostic,
// never credentials or response bodies.
type Outcome struct {
	ID       string
	Type     SourceType
	Kind     Kind
	Required bool
	Status   Status
	Error    string
	Bytes    int
}

// Report is the complete collection result in configuration order.
type Report struct {
	Documents []Document
	Outcomes  []Outcome
}

// Empty reports whether no connector produced evidence.
func (r Report) Empty() bool { return len(r.Documents) == 0 }
