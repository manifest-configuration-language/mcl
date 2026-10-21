package main

import (
	"bytes"
	"encoding/base64"
	"io/fs"
	"strings"
	"testing"

	"github.com/manifest-configuration-language/mcl/internal/wasmprotocol"
)

const binaryTestPath = "binary.dat"

func TestNewProjectFilesSupportsJSONAndBinaryFiles(t *testing.T) {
	t.Parallel()

	files := map[string]wasmprotocol.FileInput{
		"text.txt":     {Encoding: wasmprotocol.FileInputEncodingUtf8, Content: new("MCL")},
		"legacy.bin":   {Encoding: wasmprotocol.FileInputEncodingBase64, Content: new(base64.StdEncoding.EncodeToString([]byte{0, 1, 2}))},
		"transfer.bin": {Encoding: wasmprotocol.FileInputEncodingBinary},
	}
	project, err := newProjectFiles("emit {}", files, map[string][]byte{"transfer.bin": {3, 4, 5}}, "main.mcl")
	if err != nil {
		t.Fatalf("newProjectFiles() error = %v", err)
	}

	for name, want := range map[string][]byte{
		"text.txt":     []byte("MCL"),
		"legacy.bin":   {0, 1, 2},
		"transfer.bin": {3, 4, 5},
	} {
		got, err := fs.ReadFile(project, name)
		if err != nil {
			t.Fatalf("ReadFile(%q) error = %v", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("ReadFile(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestNewProjectFilesRequiresMatchingBinaryMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		files      map[string]wasmprotocol.FileInput
		binary     map[string][]byte
		wantErrSub string
	}{
		{
			name: "missing bytes",
			files: map[string]wasmprotocol.FileInput{
				binaryTestPath: {Encoding: wasmprotocol.FileInputEncodingBinary},
			},
			wantErrSub: `binary project file "` + binaryTestPath + `" is missing`,
		},
		{
			name:       "unreferenced bytes",
			binary:     map[string][]byte{binaryTestPath: {1}},
			wantErrSub: "binary file map contains files without binary metadata",
		},
		{
			name: "inline content",
			files: map[string]wasmprotocol.FileInput{
				binaryTestPath: {Encoding: wasmprotocol.FileInputEncodingBinary, Content: new("AA==")},
			},
			binary:     map[string][]byte{binaryTestPath: {1}},
			wantErrSub: "cannot include JSON content",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := newProjectFiles("emit {}", test.files, test.binary, "main.mcl")
			if err == nil || !strings.Contains(err.Error(), test.wantErrSub) {
				t.Fatalf("newProjectFiles() error = %v, want substring %q", err, test.wantErrSub)
			}
		})
	}
}
