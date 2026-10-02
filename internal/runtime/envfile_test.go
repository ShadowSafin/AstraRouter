package runtime

import (
	"os"
	"reflect"
	"testing"
)

func TestParseEnvFile(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    map[string]string
		wantErr bool
	}{
		{
			name:  "basic pairs with comments and blanks",
			input: "# a comment\n\nFOO=bar\nBAZ=qux\n",
			want:  map[string]string{"FOO": "bar", "BAZ": "qux"},
		},
		{
			name:  "export prefix is stripped",
			input: "export SYNAPASS_ADMIN_KEY=secret\n",
			want:  map[string]string{"SYNAPASS_ADMIN_KEY": "secret"},
		},
		{
			name:  "single quotes are literal",
			input: "PASSWORD='a#b$c'\n",
			want:  map[string]string{"PASSWORD": "a#b$c"},
		},
		{
			name:  "double quotes honour escapes",
			input: "MULTI=\"line1\\nline2\\ttab\\\\slash\\\"quote\"\n",
			want:  map[string]string{"MULTI": "line1\nline2\ttab\\slash\"quote"},
		},
		{
			name:  "hash inside an unquoted value is data",
			input: "URL=postgres://user:p#ss@host/db\n",
			want:  map[string]string{"URL": "postgres://user:p#ss@host/db"},
		},
		{
			name:  "empty value is allowed",
			input: "OPTIONAL=\n",
			want:  map[string]string{"OPTIONAL": ""},
		},
		{
			name:  "whitespace around key and value is trimmed",
			input: "  SPACED  =  padded  \n",
			want:  map[string]string{"SPACED": "padded"},
		},
		{
			name:    "missing equals is an error",
			input:   "NOEQUALS\n",
			wantErr: true,
		},
		{
			name:    "empty name is an error",
			input:   "=value\n",
			wantErr: true,
		},
		{
			name:    "invalid name is an error",
			input:   "HAS-DASH=value\n",
			wantErr: true,
		},
		{
			name:    "unbalanced quote is an error",
			input:   "BROKEN=\"oops\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseEnvFile([]byte(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestApplyEnvDefaultsDoesNotOverride(t *testing.T) {
	t.Setenv("RUNTIME_TEST_PRESENT", "shell")
	ApplyEnvDefaults(map[string]string{
		"RUNTIME_TEST_PRESENT": "file",
		"RUNTIME_TEST_ABSENT":  "file",
	})
	if got := os.Getenv("RUNTIME_TEST_PRESENT"); got != "shell" {
		t.Fatalf("shell value was overridden: %q", got)
	}
	if got := os.Getenv("RUNTIME_TEST_ABSENT"); got != "file" {
		t.Fatalf("file default was not applied: %q", got)
	}
}
