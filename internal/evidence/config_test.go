package evidence

import (
	"errors"
	"strings"
	"testing"
)

func TestDecodeConfig(t *testing.T) {
	valid := `{
  "schema_version": 1,
  "sources": [
    {"id":"customer-requirements","type":"file","kind":"requirement","required":true,"path":"requirements/customer.md"},
    {"id":"system-design","type":"confluence","kind":"architecture","required":false,"page_id":"12345"},
    {"id":"stage-schema","type":"postgres_schema","kind":"database_schema","required":true,"schema":"public"}
  ]
}`
	config, err := DecodeConfig(strings.NewReader(valid))
	if err != nil {
		t.Fatalf("DecodeConfig() = %v", err)
	}
	if len(config.Sources) != 3 || config.Sources[2].Schema != "public" {
		t.Fatalf("config = %+v", config)
	}
}

func TestDecodeConfigRejectsUnsafeOrDriftingInput(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"unknown field", `{"schema_version":1,"sources":[],"token":"secret"}`, "unknown field"},
		{"path escape", `{"schema_version":1,"sources":[{"id":"x","type":"file","kind":"requirement","required":true,"path":"../.env"}]}`, "must stay beneath"},
		{"absolute path", `{"schema_version":1,"sources":[{"id":"x","type":"file","kind":"requirement","required":true,"path":"/etc/passwd"}]}`, "must stay beneath"},
		{"arbitrary sql", `{"schema_version":1,"sources":[{"id":"x","type":"postgres_schema","kind":"database_schema","required":true,"schema":"public; DROP TABLE users"}]}`, "safe PostgreSQL identifier"},
		{"confluence url forbidden", `{"schema_version":1,"sources":[{"id":"x","type":"confluence","kind":"requirement","required":true,"page_id":"1","path":"https://evil.example"}]}`, "not valid for confluence"},
		{"duplicate id", `{"schema_version":1,"sources":[{"id":"same","type":"confluence","kind":"requirement","required":false,"page_id":"1"},{"id":"same","type":"confluence","kind":"requirement","required":false,"page_id":"2"}]}`, "duplicated"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeConfig(strings.NewReader(test.raw))
			if err == nil || !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("DecodeConfig() = %v, want invalid config containing %q", err, test.want)
			}
		})
	}
}
