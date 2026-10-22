package dependency

import (
	"strings"
	"testing"
)

func TestValidateGitSource(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		source string
		valid  bool
	}{
		{name: "HTTPS repository", source: "git+https://example.test/team/repo.git", valid: true},
		{name: "missing prefix", source: "https://example.test/team/repo.git"},
		{name: "insecure scheme", source: "git+http://example.test/team/repo.git"},
		{name: "userinfo", source: "git+https://user@example.test/team/repo.git"},
		{name: "query", source: "git+https://example.test/team/repo.git?token=x"},
		{name: "fragment", source: "git+https://example.test/team/repo.git#main"},
		{name: "missing host", source: "git+https:///team/repo.git"},
		{name: "backslash path", source: "git+https://example.test/team\\repo.git"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateGitSource(test.source)
			if (err == nil) != test.valid {
				t.Fatalf("ValidateGitSource(%q) error = %v, valid = %t", test.source, err, test.valid)
			}
		})
	}
}

func TestValidGitRevision(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		revision string
		valid    bool
	}{
		{name: "lowercase SHA-1", revision: strings.Repeat("a", 40), valid: true},
		{name: "uppercase", revision: "A" + strings.Repeat("a", 39)},
		{name: "short", revision: strings.Repeat("a", 39)},
		{name: "SHA-256 length", revision: strings.Repeat("a", 64)},
		{name: "non-hex", revision: strings.Repeat("g", 40)},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if valid := ValidGitRevision(test.revision); valid != test.valid {
				t.Fatalf("ValidGitRevision(%q) = %t, want %t", test.revision, valid, test.valid)
			}
		})
	}
}

func TestValidateGitDirectory(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		directory string
		valid     bool
	}{
		{name: "repository root", directory: ".", valid: true},
		{name: "nested directory", directory: "config/base", valid: true},
		{name: "empty", directory: ""},
		{name: "parent traversal", directory: "../outside"},
		{name: "embedded traversal", directory: "base/../outside"},
		{name: "absolute", directory: "/etc"},
		{name: "windows separator", directory: `base\outside`},
		{name: "duplicate separator", directory: "base//outside"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateGitDirectory(test.directory)
			if (err == nil) != test.valid {
				t.Fatalf("ValidateGitDirectory(%q) error = %v, valid = %t", test.directory, err, test.valid)
			}
		})
	}
}
