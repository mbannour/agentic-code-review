package evidence

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	SchemaVersion = 1
	MaxSources    = 20
)

// Config is an explicit, versioned list of external evidence sources. Secrets
// and remote base URLs are intentionally absent: those are operator-owned
// environment configuration and cannot be redirected by repository content.
type Config struct {
	SchemaVersion int            `json:"schema_version"`
	Sources       []SourceConfig `json:"sources"`
}

// SourceConfig is the union of the small, code-owned connector configurations.
// Fields irrelevant to a connector type are rejected rather than ignored.
type SourceConfig struct {
	ID       string     `json:"id"`
	Type     SourceType `json:"type"`
	Kind     Kind       `json:"kind"`
	Required bool       `json:"required"`

	Path   string `json:"path,omitempty"`
	PageID string `json:"page_id,omitempty"`
	Schema string `json:"schema,omitempty"`
}

var (
	idPattern        = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]{0,63}$`)
	pageIDPattern    = regexp.MustCompile(`^[0-9]+$`)
	sqlNamePattern   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]{0,62}$`)
	ErrInvalidConfig = errors.New("invalid evidence configuration")
)

// LoadConfig reads and validates a configuration file. Relative file connector
// paths are later resolved beneath the configuration file's directory.
func LoadConfig(path string) (Config, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, "", fmt.Errorf("open evidence configuration: %w", err)
	}
	defer file.Close()

	config, err := DecodeConfig(file)
	if err != nil {
		return Config{}, "", err
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return Config{}, "", fmt.Errorf("resolve evidence configuration: %w", err)
	}
	return config, filepath.Dir(abs), nil
}

// DecodeConfig strictly decodes and validates a configuration.
func DecodeConfig(reader io.Reader) (Config, error) {
	decoder := json.NewDecoder(io.LimitReader(reader, 1024*1024))
	decoder.DisallowUnknownFields()

	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("%w: decode: %v", ErrInvalidConfig, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Config{}, fmt.Errorf("%w: unexpected content after JSON object", ErrInvalidConfig)
		}
		return Config{}, fmt.Errorf("%w: trailing content: %v", ErrInvalidConfig, err)
	}

	if err := ValidateConfig(config); err != nil {
		return Config{}, err
	}
	return config, nil
}

// ValidateConfig rejects ambiguous, unsafe, and connector-inappropriate fields.
func ValidateConfig(config Config) error {
	var problems []string
	if config.SchemaVersion != SchemaVersion {
		problems = append(problems, fmt.Sprintf("schema_version must be %d", SchemaVersion))
	}
	if len(config.Sources) > MaxSources {
		problems = append(problems, fmt.Sprintf("%d sources exceed the limit of %d", len(config.Sources), MaxSources))
	}

	seen := map[string]bool{}
	for i, source := range config.Sources {
		prefix := fmt.Sprintf("sources[%d]", i)
		if !idPattern.MatchString(source.ID) {
			problems = append(problems, prefix+".id must match "+idPattern.String())
		} else if seen[source.ID] {
			problems = append(problems, prefix+".id is duplicated")
		}
		seen[source.ID] = true

		if !source.Type.Valid() {
			problems = append(problems, prefix+".type is unsupported")
		}
		if !source.Kind.Valid() {
			problems = append(problems, prefix+".kind is unsupported")
		}

		switch source.Type {
		case SourceFile:
			if strings.TrimSpace(source.Path) == "" {
				problems = append(problems, prefix+".path is required for file")
			}
			if filepath.IsAbs(source.Path) || escapesRoot(source.Path) {
				problems = append(problems, prefix+".path must stay beneath the configuration directory")
			}
			if source.PageID != "" || source.Schema != "" {
				problems = append(problems, prefix+" contains fields not valid for file")
			}

		case SourceConfluence:
			if !pageIDPattern.MatchString(source.PageID) {
				problems = append(problems, prefix+".page_id must contain digits only")
			}
			if source.Path != "" || source.Schema != "" {
				problems = append(problems, prefix+" contains fields not valid for confluence")
			}

		case SourcePostgresSchema:
			if source.Kind != KindDatabaseSchema {
				problems = append(problems, prefix+".kind must be database_schema for postgres_schema")
			}
			if !sqlNamePattern.MatchString(source.Schema) {
				problems = append(problems, prefix+".schema is not a safe PostgreSQL identifier")
			}
			if source.Path != "" || source.PageID != "" {
				problems = append(problems, prefix+" contains fields not valid for postgres_schema")
			}
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalidConfig, strings.Join(problems, "; "))
	}
	return nil
}

func escapesRoot(path string) bool {
	clean := filepath.Clean(strings.TrimSpace(path))
	return clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator))
}
