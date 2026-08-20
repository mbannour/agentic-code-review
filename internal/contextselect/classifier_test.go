package contextselect

import (
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		path string
		want FileKind
	}{
		// Source.
		{name: "go source", path: "internal/payment/service.go", want: FileKindSource},
		{name: "go source at the root", path: "main.go", want: FileKindSource},
		{name: "go source named like a test package", path: "internal/testutil/helper.go", want: FileKindSource},
		{name: "proto definition", path: "api/payment.proto", want: FileKindSource},
		{name: "typescript source", path: "web/src/app.ts", want: FileKindSource},
		{name: "python source", path: "scripts/import.py", want: FileKindSource},

		// Tests.
		{name: "go test", path: "internal/payment/service_test.go", want: FileKindTest},
		{name: "go test at the root", path: "main_test.go", want: FileKindTest},
		{name: "typescript test", path: "web/src/app.test.ts", want: FileKindTest},
		{name: "python test in a tests directory", path: "tests/test_import.py", want: FileKindTest},
		{name: "javascript spec", path: "web/src/app.spec.js", want: FileKindTest},

		// Dependency manifests.
		{name: "go.mod", path: "go.mod", want: FileKindDependency},
		{name: "go.sum", path: "go.sum", want: FileKindDependency},
		{name: "go.work", path: "go.work", want: FileKindDependency},
		{name: "nested go.mod", path: "tools/go.mod", want: FileKindDependency},
		{name: "package.json", path: "web/package.json", want: FileKindDependency},
		{name: "cargo lock", path: "Cargo.lock", want: FileKindDependency},

		// Documentation.
		{name: "README", path: "README.md", want: FileKindDocumentation},
		{name: "markdown anywhere", path: "internal/payment/NOTES.md", want: FileKindDocumentation},
		{name: "docs directory", path: "docs/architecture.md", want: FileKindDocumentation},
		{name: "non-markdown file in docs", path: "docs/diagram.svg", want: FileKindDocumentation},
		{name: "nested docs directory", path: "internal/docs/design.txt", want: FileKindDocumentation},
		{name: "CHANGELOG", path: "CHANGELOG.md", want: FileKindDocumentation},
		{name: "AGENTS.md", path: "AGENTS.md", want: FileKindDocumentation},
		{name: "restructured text", path: "guide.rst", want: FileKindDocumentation},

		// Config.
		{name: "yaml", path: "deploy/values.yaml", want: FileKindConfig},
		{name: "yml", path: ".github/workflows/ci.yml", want: FileKindConfig},
		{name: "json", path: "config/settings.json", want: FileKindConfig},
		{name: "toml", path: "config/app.toml", want: FileKindConfig},
		{name: "Dockerfile", path: "Dockerfile", want: FileKindConfig},
		{name: "nested Dockerfile", path: "build/Dockerfile", want: FileKindConfig},
		{name: "Dockerfile with a suffix", path: "Dockerfile.prod", want: FileKindConfig},
		{name: "docker-compose", path: "docker-compose.yml", want: FileKindConfig},
		{name: "docker-compose override", path: "docker-compose.override.yml", want: FileKindConfig},
		{name: "Makefile", path: "Makefile", want: FileKindConfig},
		{name: "shell script", path: "scripts/deploy.sh", want: FileKindConfig},
		{name: "golangci config", path: ".golangci.yml", want: FileKindConfig},

		// Migrations.
		{name: "sql migration", path: "migrations/0042_retry.sql", want: FileKindMigration},
		{name: "go migration in a migrations directory", path: "migrations/0043_add_index.go", want: FileKindMigration},
		{name: "nested migrations directory", path: "internal/db/migrations/0001_init.sql", want: FileKindMigration},
		{name: "db/migrate directory", path: "db/migrate/20240101_create.rb", want: FileKindMigration},
		{name: "standalone sql file", path: "queries/report.sql", want: FileKindMigration},

		// Generated.
		{name: "protobuf go", path: "api/payment.pb.go", want: FileKindGenerated},
		{name: "grpc gateway", path: "api/payment.pb.gw.go", want: FileKindGenerated},
		{name: "generated suffix", path: "internal/mock/store_generated.go", want: FileKindGenerated},
		{name: "gen suffix", path: "internal/mock/store_gen.go", want: FileKindGenerated},
		{name: "stringer output", path: "internal/kind_string.go", want: FileKindGenerated},
		{name: "vendored code", path: "vendor/github.com/x/y/z.go", want: FileKindGenerated},
		{name: "node modules", path: "web/node_modules/pkg/index.js", want: FileKindGenerated},
		{name: "generated directory", path: "internal/generated/client.go", want: FileKindGenerated},
		{name: "minified javascript", path: "web/dist/app.min.js", want: FileKindGenerated},

		// Unknown.
		{name: "binary asset", path: "assets/logo.png", want: FileKindUnknown},
		{name: "no extension", path: "LICENSE-THIRD-PARTY", want: FileKindUnknown},
		{name: "unfamiliar extension", path: "data/export.parquet", want: FileKindUnknown},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.path, ""); got != tt.want {
				t.Errorf("Classify(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

// TestClassifyGeneratedByMarker covers header-based detection of generated code.
func TestClassifyGeneratedByMarker(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		patch string
		want  FileKind
	}{
		{
			name:  "go generated header",
			path:  "internal/api/client.go",
			patch: "@@ -1,4 +1,6 @@\n+// Code generated by oapi-codegen. DO NOT EDIT.\n+package api\n",
			want:  FileKindGenerated,
		},
		{
			name:  "DO NOT EDIT alone",
			path:  "internal/api/client.go",
			patch: "@@ -1,3 +1,5 @@\n+// DO NOT EDIT.\n+package api\n",
			want:  FileKindGenerated,
		},
		{
			name:  "at-generated annotation",
			path:  "internal/api/client.go",
			patch: "@@ -1,3 +1,5 @@\n+// @generated\n+package api\n",
			want:  FileKindGenerated,
		},
		{
			name:  "hash comment in yaml",
			path:  "deploy/values.yaml",
			patch: "@@ -1,2 +1,4 @@\n+# Code generated by helm. DO NOT EDIT.\n+replicas: 3\n",
			want:  FileKindGenerated,
		},
		{
			name:  "marker deep in the patch is ignored",
			path:  "internal/api/client.go",
			patch: "@@ -1,2 +1,2 @@\n" + strings.Repeat("+ordinary line\n", 30) + "+// Code generated by tool. DO NOT EDIT.\n",
			want:  FileKindSource,
		},
		{
			name:  "prose mentioning generated code is not generated",
			path:  "docs/codegen.md",
			patch: "@@ -1,2 +1,3 @@\n+We use Code generated by protoc for the API.\n",
			want:  FileKindDocumentation,
		},
		{
			name:  "non-comment line mentioning the marker is ignored",
			path:  "internal/api/client.go",
			patch: "@@ -1,2 +1,3 @@\n+const banner = \"Code generated by tool. DO NOT EDIT.\"\n",
			want:  FileKindSource,
		},
		{
			name:  "empty patch does not trigger detection",
			path:  "internal/api/client.go",
			patch: "",
			want:  FileKindSource,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.path, tt.patch); got != tt.want {
				t.Errorf("Classify(%q, patch) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

// TestClassifyIsDeterministic pins repeatability.
func TestClassifyIsDeterministic(t *testing.T) {
	paths := []string{
		"internal/payment/service.go", "internal/payment/service_test.go",
		"migrations/0042.sql", "README.md", "go.mod", "api/x.pb.go", "assets/logo.png",
	}

	for _, p := range paths {
		first := Classify(p, "")
		for i := 0; i < 10; i++ {
			if got := Classify(p, ""); got != first {
				t.Fatalf("Classify(%q) returned %q then %q", p, first, got)
			}
		}
	}
}

// TestClassifyHandlesOddPaths checks unusual input does not panic or misbehave.
func TestClassifyHandlesOddPaths(t *testing.T) {
	for _, p := range []string{"", "/", ".", "..", "/absolute/path/x.go", "./relative/x.go", "a/../b/x.go", strings.Repeat("deep/", 50) + "x.go"} {
		got := Classify(p, "")
		if got == "" {
			t.Errorf("Classify(%q) returned an empty kind", p)
		}
	}

	// Case should not matter for recognised names.
	if got := Classify("readme.MD", ""); got != FileKindDocumentation {
		t.Errorf("Classify(%q) = %q, want documentation", "readme.MD", got)
	}
	if got := Classify("DOCKERFILE", ""); got != FileKindConfig {
		t.Errorf("Classify(%q) = %q, want config", "DOCKERFILE", got)
	}
}

// TestClassifyVendoredGoIsNotSource is the ordering guarantee: a vendored .go file
// must not be promoted to source priority.
func TestClassifyVendoredGoIsNotSource(t *testing.T) {
	paths := []string{
		"vendor/github.com/x/y/y.go",
		"vendor/github.com/x/y/y_test.go",
		"third_party/lib/lib.go",
	}

	for _, p := range paths {
		if got := Classify(p, ""); got != FileKindGenerated {
			t.Errorf("Classify(%q) = %q, want generated", p, got)
		}
	}
}

func TestImportanceFor(t *testing.T) {
	tests := []struct {
		kind FileKind
		want Importance
	}{
		{kind: FileKindSource, want: ImportanceHigh},
		{kind: FileKindTest, want: ImportanceHigh},
		{kind: FileKindMigration, want: ImportanceHigh},
		{kind: FileKindConfig, want: ImportanceMedium},
		{kind: FileKindDependency, want: ImportanceMedium},
		{kind: FileKindUnknown, want: ImportanceMedium},
		{kind: FileKindDocumentation, want: ImportanceLow},
		{kind: FileKindGenerated, want: ImportanceLow},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(string(tt.kind), func(t *testing.T) {
			if got := ImportanceFor(tt.kind); got != tt.want {
				t.Errorf("ImportanceFor(%q) = %q, want %q", tt.kind, got, tt.want)
			}
		})
	}
}

// TestKindRankOrdering pins the documented priority sequence.
func TestKindRankOrdering(t *testing.T) {
	order := []FileKind{
		FileKindSource, FileKindTest, FileKindMigration, FileKindConfig,
		FileKindUnknown, FileKindDependency, FileKindDocumentation, FileKindGenerated,
	}

	for i := 1; i < len(order); i++ {
		if kindRank(order[i-1]) >= kindRank(order[i]) {
			t.Errorf("kindRank(%q) = %d is not before kindRank(%q) = %d",
				order[i-1], kindRank(order[i-1]), order[i], kindRank(order[i]))
		}
	}
}

func TestImportanceDisplay(t *testing.T) {
	tests := []struct {
		importance Importance
		want       string
	}{
		{importance: ImportanceHigh, want: "HIGH"},
		{importance: ImportanceMedium, want: "MED"},
		{importance: ImportanceLow, want: "LOW"},
		{importance: Importance("weird"), want: "?"},
	}

	for _, tt := range tests {
		if got := tt.importance.Display(); got != tt.want {
			t.Errorf("Display() = %q, want %q", got, tt.want)
		}
	}
}

func TestReasonFor(t *testing.T) {
	tests := []struct {
		kind FileKind
		want string
	}{
		{kind: FileKindSource, want: "changed source file"},
		{kind: FileKindTest, want: "changed test file"},
		{kind: FileKindMigration, want: "database migration"},
		{kind: FileKindConfig, want: "configuration change"},
		{kind: FileKindDependency, want: "dependency change"},
		{kind: FileKindDocumentation, want: "documentation"},
		{kind: FileKindGenerated, want: "generated file"},
		{kind: FileKindUnknown, want: "changed file"},
	}

	for _, tt := range tests {
		if got := reasonFor(tt.kind); got != tt.want {
			t.Errorf("reasonFor(%q) = %q, want %q", tt.kind, got, tt.want)
		}
	}
}
