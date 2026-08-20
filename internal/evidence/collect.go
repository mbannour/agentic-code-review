package evidence

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrRequiredSource is returned when a required connector cannot produce usable
// evidence. Optional failures remain visible in Report.Outcomes but do not stop
// the review.
var ErrRequiredSource = errors.New("required evidence source failed")

// Collector gathers connectors sequentially in configuration order. Stable
// order makes prompts and audit output reproducible and keeps load on customer
// systems bounded.
type Collector struct{}

func NewCollector() Collector { return Collector{} }

func (Collector) Collect(ctx context.Context, connectors []Connector) (Report, error) {
	var report Report
	var requiredFailures []string

	for _, connector := range connectors {
		if err := ctx.Err(); err != nil {
			return report, err
		}

		outcome := Outcome{
			ID: connector.ID(), Type: connector.Type(), Kind: connector.Kind(),
			Required: connector.Required(),
		}
		document, err := connector.Collect(ctx)
		switch {
		case err != nil:
			outcome.Status = StatusFailed
			outcome.Error = safeError(err)
			if connector.Required() {
				requiredFailures = append(requiredFailures, connector.ID()+": "+outcome.Error)
			}
		case document.Empty():
			outcome.Status = StatusSkipped
			outcome.Error = "source returned no usable content"
			if connector.Required() {
				requiredFailures = append(requiredFailures, connector.ID()+": "+outcome.Error)
			}
		default:
			outcome.Status = StatusCollected
			outcome.Bytes = len(document.Content)
			report.Documents = append(report.Documents, document)
		}
		report.Outcomes = append(report.Outcomes, outcome)
	}

	if len(requiredFailures) > 0 {
		return report, fmt.Errorf("%w: %s", ErrRequiredSource, strings.Join(requiredFailures, "; "))
	}
	return report, nil
}

func safeError(err error) string {
	if err == nil {
		return ""
	}
	// Connector errors are constructed without response bodies, command
	// environments, or credentials. Flattening newlines keeps terminal output
	// one-record-per-source.
	return strings.Join(strings.Fields(err.Error()), " ")
}

// BuildConnectors turns validated configuration into narrowly capable readers.
func BuildConnectors(config Config, root string) ([]Connector, error) {
	connectors := make([]Connector, 0, len(config.Sources))
	for _, source := range config.Sources {
		var connector Connector
		switch source.Type {
		case SourceFile:
			built, err := NewFileConnector(source, root)
			if err != nil {
				return nil, fmt.Errorf("configure evidence source %s: %w", source.ID, err)
			}
			connector = built
		case SourceConfluence:
			connector = NewConfluenceConnector(source, nil)
		case SourcePostgresSchema:
			connector = NewPostgresSchemaConnector(source, nil)
		default:
			return nil, fmt.Errorf("configure evidence source %s: unsupported type %q", source.ID, source.Type)
		}
		connectors = append(connectors, connector)
	}
	return connectors, nil
}

// CollectConfigured is the CLI entry point for loading and collecting one file.
func CollectConfigured(ctx context.Context, path string) (Report, error) {
	config, root, err := LoadConfig(path)
	if err != nil {
		return Report{}, err
	}
	connectors, err := BuildConnectors(config, root)
	if err != nil {
		return Report{}, err
	}
	return NewCollector().Collect(ctx, connectors)
}
