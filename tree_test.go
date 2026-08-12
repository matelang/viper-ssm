package viperssm

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

func TestDocument(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		params []types.Parameter
		want   string
	}{
		{
			name:   "three levels of nesting",
			prefix: "/app/prod",
			params: []types.Parameter{
				strParam("/app/prod/database/primary/host", "db.example", 1),
				strParam("/app/prod/database/primary/port", "5432", 1),
				strParam("/app/prod/log_level", "info", 1),
			},
			want: `{"database":{"primary":{"host":"db.example","port":"5432"}},"log_level":"info"}`,
		},
		{
			name:   "no parameters is an empty document",
			prefix: "/app/prod",
			params: nil,
			want:   `{}`,
		},
		{
			name:   "root prefix",
			prefix: "/",
			params: []types.Parameter{strParam("/app/host", "example", 1)},
			want:   `{"app":{"host":"example"}}`,
		},
		{
			name:   "original casing survives, since Viper lowercases keys itself",
			prefix: "/app",
			params: []types.Parameter{strParam("/app/Database/Host", "example", 1)},
			want:   `{"Database":{"Host":"example"}}`,
		},
		{
			name:   "SecureString is a plain string",
			prefix: "/app",
			params: []types.Parameter{secureParam("/app/token", "abc123", 4)},
			want:   `{"token":"abc123"}`,
		},
		{
			name:   "StringList becomes an array, split on commas and not trimmed",
			prefix: "/app",
			params: []types.Parameter{listParam("/app/hosts", "a.example, b.example", 1)},
			want:   `{"hosts":["a.example"," b.example"]}`,
		},
		{
			name:   "empty StringList is an empty array",
			prefix: "/app",
			params: []types.Parameter{listParam("/app/hosts", "", 1)},
			want:   `{"hosts":[]}`,
		},
		{
			name:   "numbers and booleans stay strings for Viper to convert",
			prefix: "/app",
			params: []types.Parameter{
				strParam("/app/port", "8080", 1),
				strParam("/app/debug", "true", 1),
			},
			want: `{"debug":"true","port":"8080"}`,
		},
	}

	p := newProvider(t, &fakeSSM{})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := p.document(tt.prefix, tt.params)
			if err != nil {
				t.Fatalf("document: %v", err)
			}
			if string(doc) != tt.want {
				t.Errorf("document =\n  %s\nwant\n  %s", doc, tt.want)
			}
		})
	}
}

func TestDocumentCollision(t *testing.T) {
	p := newProvider(t, &fakeSSM{})

	tests := []struct {
		name       string
		params     []types.Parameter
		wantKey    string
		wantLeaf   string
		wantPrefix string
	}{
		{
			name: "value arrives before the prefix that needs its key",
			params: []types.Parameter{
				strParam("/app/db", "postgres://example", 1),
				strParam("/app/db/host", "example", 1),
			},
			wantKey:    "db",
			wantLeaf:   "/app/db",
			wantPrefix: "/app/db/host",
		},
		{
			name: "prefix arrives before the value that claims its key",
			params: []types.Parameter{
				strParam("/app/db/host", "example", 1),
				strParam("/app/db", "postgres://example", 1),
			},
			wantKey:    "db",
			wantLeaf:   "/app/db",
			wantPrefix: "/app/db/host",
		},
		{
			name: "collision two levels down",
			params: []types.Parameter{
				strParam("/app/a/b/c", "leaf", 1),
				strParam("/app/a/b", "value", 1),
			},
			wantKey:    "a.b",
			wantLeaf:   "/app/a/b",
			wantPrefix: "/app/a/b/c",
		},
		{
			name: "the reported parameter is found several levels below the key",
			params: []types.Parameter{
				strParam("/app/a/b/c/d", "leaf", 1),
				strParam("/app/a", "value", 1),
			},
			wantKey:    "a",
			wantLeaf:   "/app/a",
			wantPrefix: "/app/a/b/c/d",
		},
		{
			name: "the reported parameter is chosen deterministically",
			params: []types.Parameter{
				strParam("/app/a/z", "last", 1),
				strParam("/app/a/b/deep", "first", 1),
				strParam("/app/a", "value", 1),
			},
			wantKey:    "a",
			wantLeaf:   "/app/a",
			wantPrefix: "/app/a/b/deep",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := p.document("/app", tt.params)

			var collision *CollisionError
			if !errors.As(err, &collision) {
				t.Fatalf("document error = %v, want *CollisionError", err)
			}
			if collision.Key != tt.wantKey {
				t.Errorf("Key = %q, want %q", collision.Key, tt.wantKey)
			}
			if collision.Leaf != tt.wantLeaf {
				t.Errorf("Leaf = %q, want %q", collision.Leaf, tt.wantLeaf)
			}
			if collision.Prefix != tt.wantPrefix {
				t.Errorf("Prefix = %q, want %q", collision.Prefix, tt.wantPrefix)
			}
		})
	}
}

// TestDocumentDuplicateKey covers the case Viper would resolve silently:
// parameter names are case-sensitive in SSM and keys are not in Viper, so two
// parameters can claim one key.
func TestDocumentDuplicateKey(t *testing.T) {
	p := newProvider(t, &fakeSSM{})

	_, err := p.document("/app", []types.Parameter{
		strParam("/app/db/HOST", "first", 1),
		strParam("/app/db/host", "second", 1),
	})

	var duplicate *DuplicateKeyError
	if !errors.As(err, &duplicate) {
		t.Fatalf("document error = %v, want *DuplicateKeyError", err)
	}
	if duplicate.Key != "db.host" {
		t.Errorf("Key = %q, want %q", duplicate.Key, "db.host")
	}
	if duplicate.First != "/app/db/HOST" || duplicate.Second != "/app/db/host" {
		t.Errorf("named %q and %q, want /app/db/HOST and /app/db/host", duplicate.First, duplicate.Second)
	}
}

func TestDocumentInvalidName(t *testing.T) {
	p := newProvider(t, &fakeSSM{})

	tests := []struct {
		name   string
		prefix string
		param  types.Parameter
	}{
		{name: "empty path segment", prefix: "/app", param: strParam("/app//host", "example", 1)},
		{name: "not below the prefix", prefix: "/app", param: strParam("/other/host", "example", 1)},
		{name: "equal to the prefix", prefix: "/app", param: strParam("/app", "example", 1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := p.document(tt.prefix, []types.Parameter{tt.param})

			var invalid *InvalidNameError
			if !errors.As(err, &invalid) {
				t.Fatalf("document error = %v, want *InvalidNameError", err)
			}
			if invalid.Name != *tt.param.Name {
				t.Errorf("Name = %q, want %q", invalid.Name, *tt.param.Name)
			}
		})
	}
}

// TestDocumentSkipsNamelessParameter keeps one malformed entry from failing a
// whole configuration load. There is no name to report, so there is no error to
// raise that would help anyone.
func TestDocumentSkipsNamelessParameter(t *testing.T) {
	logger, logs := captureLogs()
	p := newProvider(t, &fakeSSM{}, WithLogger(logger))

	doc, err := p.document("/app", []types.Parameter{
		{Value: nil, Type: types.ParameterTypeString},
		strParam("/app/host", "example", 1),
	})
	if err != nil {
		t.Fatalf("document: %v", err)
	}
	if got, want := string(doc), `{"host":"example"}`; got != want {
		t.Errorf("document = %s, want %s", got, want)
	}
	if logs.Len() == 0 {
		t.Error("skipping a nameless parameter logged nothing")
	}
}

func TestNormalizePath(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr error
	}{
		{in: "/app/prod", want: "/app/prod"},
		{in: "/app/prod/", want: "/app/prod"},
		{in: "/app/prod///", want: "/app/prod"},
		{in: "app/prod", want: "/app/prod"},
		{in: "  /app/prod  ", want: "/app/prod"},
		{in: "/", want: "/"},
		{in: "///", want: "/"},
		{in: "", wantErr: ErrEmptyPath},
		{in: "   ", wantErr: ErrEmptyPath},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := normalizePath(tt.in)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("normalizePath(%q) error = %v, want %v", tt.in, err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Errorf("normalizePath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestDocumentTrailingSlashPrefix proves the prefix is stripped at the same
// boundary however the caller wrote it.
func TestDocumentTrailingSlashPrefix(t *testing.T) {
	p := newProvider(t, &fakeSSM{})
	params := []types.Parameter{strParam("/app/prod/db/host", "example", 1)}

	for _, prefix := range []string{"/app/prod", "/app/prod/"} {
		normalized, err := normalizePath(prefix)
		if err != nil {
			t.Fatalf("normalizePath(%q): %v", prefix, err)
		}
		doc, err := p.document(normalized, params)
		if err != nil {
			t.Fatalf("document(%q): %v", prefix, err)
		}
		if got, want := string(doc), `{"db":{"host":"example"}}`; got != want {
			t.Errorf("prefix %q: document = %s, want %s", prefix, got, want)
		}
	}
}
