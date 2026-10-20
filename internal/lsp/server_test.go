package lsp

import (
	"bufio"
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/manifest-configuration-language/mcl/internal/engine"
	"github.com/manifest-configuration-language/mcl/internal/syntax"
	"github.com/manifest-configuration-language/mcl/pkg/mcl"
)

const renamedTypeName = "Renamed"

// テストでサーバへ送るJSON-RPCメッセージ。
const (
	initializeRequest = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	exitRequest       = `{"jsonrpc":"2.0","method":"exit"}`
)

func TestRunPublishesDiagnosticsForOpenDocument(t *testing.T) {
	t.Parallel()
	session := newTestLSPConnection()
	defer session.close(t)
	session.send(t, initializeRequest)
	initialize := session.receive(t)
	for _, expected := range []string{`"textDocumentSync":`, `"openClose":true`, `"change":0`} {
		if !strings.Contains(initialize, expected) {
			t.Errorf("initialize response does not contain %q: %s", expected, initialize)
		}
	}
	session.send(t, `{"jsonrpc":"2.0","method":"textDocument/didOpen","params":{"textDocument":{"uri":"file:///main.mcl","version":1,"text":"let value = missing\nemit value\n"}}}`)
	diagnostics := session.receive(t)
	for _, expected := range []string{`textDocument/publishDiagnostics`, `undefined name`} {
		if !strings.Contains(diagnostics, expected) {
			t.Errorf("diagnostic notification does not contain %q: %s", expected, diagnostics)
		}
	}
	session.shutdown(t)
}

func TestCollectSourceDiagnosticsFromJoinedErrors(t *testing.T) {
	t.Parallel()
	first := mcl.SourceDiagnosticError{Code: "MCL-ONE", Message: "first", Span: syntax.SourceSpan{Start: 1, End: 2}}
	second := mcl.SourceDiagnosticError{Code: "MCL-TWO", Message: "second", Span: syntax.SourceSpan{Start: 3, End: 4}}
	got := collectSourceDiagnostics(fmt.Errorf("wrapped: %w", errors.Join(first, second)))
	if len(got) != 2 || got[0].Code != first.Code || got[1].Code != second.Code {
		t.Fatalf("collected diagnostics = %#v, want both joined diagnostics in order", got)
	}
}

func TestDocumentDiagnosticsSkipEvaluationUntilRequested(t *testing.T) {
	t.Parallel()
	uri := filePathToURI(filepath.Join(t.TempDir(), "main.mcl"))
	s := &server{}
	_, staticDiagnostics, _ := s.diagnosticsForDocument(t.Context(), uri, "emit missing\n", false)
	for _, diagnostic := range staticDiagnostics {
		if strings.Contains(diagnostic.Message, "undefined name") {
			t.Fatalf("static diagnostics unexpectedly evaluated the source: %#v", staticDiagnostics)
		}
	}
	_, evaluatedDiagnostics, _ := s.diagnosticsForDocument(t.Context(), uri, "emit missing\n", true)
	foundUndefinedName := slices.ContainsFunc(evaluatedDiagnostics, func(diagnostic diagnostic) bool {
		return strings.Contains(diagnostic.Message, "undefined name")
	})
	if !foundUndefinedName {
		t.Fatalf("evaluated diagnostics = %#v, want the undefined name error", evaluatedDiagnostics)
	}
}

func TestPublishDiagnosticResultClearsOldURIOneTime(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	s := server{
		writer:         &output,
		documents:      map[string]documentState{"file:///current.mcl": {Version: 1, Diagnostics: map[string][]diagnostic{"file:///current.mcl": {}}}},
		diagnosticURIs: map[string]struct{}{"file:///old.mcl": {}},
	}
	result := diagnosticResult{versions: map[string]int{"file:///current.mcl": 1}}
	if err := s.publishDiagnosticResult(result); err != nil {
		t.Fatal(err)
	}
	if uris := diagnosticNotificationURIs(t, output.Bytes()); !slices.Contains(uris, "file:///old.mcl") {
		t.Fatalf("first publication omitted stale URI clear: %#v", uris)
	}
	if _, exists := s.diagnosticURIs["file:///old.mcl"]; exists {
		t.Fatal("cleared URI remained in the active diagnostic set")
	}
	output.Reset()
	if err := s.publishDiagnosticResult(result); err != nil {
		t.Fatal(err)
	}
	if uris := diagnosticNotificationURIs(t, output.Bytes()); slices.Contains(uris, "file:///old.mcl") {
		t.Fatalf("second publication repeated stale URI clear: %#v", uris)
	}
}

func diagnosticNotificationURIs(t *testing.T, output []byte) []string {
	t.Helper()
	reader := bufio.NewReader(bytes.NewReader(output))
	var uris []string
	for {
		if _, err := reader.Peek(1); errors.Is(err, io.EOF) {
			return uris
		}
		contentLength := -1
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				break
			}
			name, value, ok := strings.Cut(line, ":")
			if ok && strings.EqualFold(name, "Content-Length") {
				contentLength, err = strconv.Atoi(strings.TrimSpace(value))
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		if contentLength < 0 {
			t.Fatal("LSP notification omitted Content-Length")
		}
		body := make([]byte, contentLength)
		if _, err := io.ReadFull(reader, body); err != nil {
			t.Fatal(err)
		}
		var notification struct {
			Params struct {
				URI string `json:"uri"`
			} `json:"params"`
		}
		if err := json.Unmarshal(body, &notification); err != nil {
			t.Fatal(err)
		}
		uris = append(uris, notification.Params.URI)
	}
}

func TestWatchedFileChangesScheduleDependentDiagnostics(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dependency := filepath.Join(root, "dependency.mcl")
	if err := os.WriteFile(dependency, []byte("emit \"ok\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dependency, err := filepath.EvalSymlinks(dependency)
	if err != nil {
		t.Fatal(err)
	}
	uri := filePathToURI(filepath.Join(root, "main.mcl"))
	results := make(chan diagnosticResult, 1)
	s := server{documents: map[string]documentState{uri: {Text: "emit \"ok\"\n", Version: 1, Dependencies: []string{dependency}}}}
	s.scheduleDiagnosticsForChanges([]string{filePathToURI(dependency)}, results)
	select {
	case result := <-results:
		if _, exists := result.states[uri]; !exists {
			t.Fatalf("watched dependency did not schedule dependent document: %#v", result.states)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("diagnostics were not scheduled for the dependent document")
	}
}

func TestFileURIToPathWindowsForms(t *testing.T) {
	t.Parallel()
	tests := []struct {
		uri  string
		want string
	}{
		{uri: "file:///C:/work/a%20b/%E9%9B%AA.mcl", want: `C:\work\a b\雪.mcl`},
		{uri: "file://server/share/work.mcl", want: `\\server\share\work.mcl`},
	}
	for _, test := range tests {
		t.Run(test.uri, func(t *testing.T) {
			t.Parallel()
			got, ok := fileURIToPathForOS(test.uri, "windows")
			if !ok || got != test.want {
				t.Fatalf("fileURIToPathForOS(%q) = (%q, %t), want (%q, true)", test.uri, got, ok, test.want)
			}
		})
	}
}

func TestFilePathToURIWindowsForms(t *testing.T) {
	t.Parallel()
	tests := []struct {
		path string
		want string
	}{
		{path: `C:\work\a b\雪.mcl`, want: "file:///C:/work/a%20b/%E9%9B%AA.mcl"},
		{path: `\\server\share\work.mcl`, want: "file://server/share/work.mcl"},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			t.Parallel()
			if got := filePathToURIForOS(test.path, "windows"); got != test.want {
				t.Fatalf("filePathToURIForOS(%q) = %q, want %q", test.path, got, test.want)
			}
		})
	}
}

func TestDidChangeRejectsNonIncreasingVersionsAndResynchronizesFullText(t *testing.T) {
	t.Parallel()
	documents := map[string]documentState{"file:///main.mcl": {Text: "old", Version: 4}}
	s := server{documents: documents}
	params := `{"textDocument":{"uri":"file:///main.mcl","version":4},"contentChanges":[{"text":"duplicate"}]}`
	if err := s.handle(request{Method: "textDocument/didChange", Params: jsontext.Value(params)}, make(chan diagnosticResult, 1)); err != nil {
		t.Fatal(err)
	}
	if got := s.documents["file:///main.mcl"].Text; got != "old" {
		t.Fatalf("duplicate version changed document to %q", got)
	}
	params = `{"textDocument":{"uri":"file:///main.mcl","version":5},"contentChanges":[{"range":{"start":{"line":9,"character":0},"end":{"line":9,"character":0}},"text":"bad"}]}`
	if err := s.handle(request{Method: "textDocument/didChange", Params: jsontext.Value(params)}, make(chan diagnosticResult, 1)); err != nil {
		t.Fatal(err)
	}
	if !s.documents["file:///main.mcl"].SyncLost {
		t.Fatal("invalid range did not mark document synchronization as lost")
	}
	params = `{"textDocument":{"uri":"file:///main.mcl","version":6},"contentChanges":[{"text":"resynchronized"}]}`
	if err := s.handle(request{Method: "textDocument/didChange", Params: jsontext.Value(params)}, make(chan diagnosticResult, 1)); err != nil {
		t.Fatal(err)
	}
	if state := s.documents["file:///main.mcl"]; state.Text != "resynchronized" || state.SyncLost {
		t.Fatalf("full-text resynchronization state = %#v", state)
	}
}

func TestRunRejectsOversizedMessages(t *testing.T) {
	t.Parallel()
	var input strings.Builder
	fmt.Fprintf(&input, "Content-Length: %d\r\n\r\n", maximumMessageSize+1)
	var output bytes.Buffer
	if err := Run(strings.NewReader(input.String()), &output); err == nil {
		t.Fatal("Run accepted an oversized message; want an error")
	}
}

func TestRunRefreshesDiagnosticsAfterChangesAndClose(t *testing.T) {
	t.Parallel()
	session := newTestLSPConnection()
	defer session.close(t)
	session.send(t, initializeRequest)
	session.receive(t)
	session.send(t, `{"jsonrpc":"2.0","method":"textDocument/didOpen","params":{"textDocument":{"uri":"file:///main.mcl","version":1,"text":"let value = missing\nemit value\n"}}}`)
	opened := session.receive(t)
	session.send(t, `{"jsonrpc":"2.0","method":"textDocument/didChange","params":{"textDocument":{"uri":"file:///main.mcl","version":2},"contentChanges":[{"text":"let value = \"ok\"\nemit value\n"}]}}`)
	changed := session.receive(t)
	session.send(t, `{"jsonrpc":"2.0","method":"textDocument/didClose","params":{"textDocument":{"uri":"file:///main.mcl"}}}`)
	closed := session.receive(t)
	for _, output := range []string{opened, changed, closed} {
		if !strings.Contains(output, `"method":"textDocument/publishDiagnostics"`) {
			t.Fatalf("expected a diagnostics notification, got: %s", output)
		}
	}
	if !strings.Contains(closed, `"diagnostics":[]`) {
		t.Fatalf("expected diagnostics to be cleared: %s", closed)
	}
	session.shutdown(t)
}

type testLSPConnection struct {
	input      *io.PipeWriter
	output     *bufio.Reader
	outputPipe *io.PipeReader
	done       chan error
	finished   bool
}

func newTestLSPConnection() *testLSPConnection {
	inputReader, input := io.Pipe()
	outputReader, output := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- Run(inputReader, output)
	}()

	return &testLSPConnection{input: input, output: bufio.NewReader(outputReader), outputPipe: outputReader, done: done}
}

func (connection *testLSPConnection) send(t *testing.T, body string) {
	t.Helper()
	if _, err := fmt.Fprintf(connection.input, "Content-Length: %d\r\n\r\n%s", len(body), body); err != nil {
		t.Fatal(err)
	}
}

func (connection *testLSPConnection) receive(t *testing.T) string {
	t.Helper()
	contentLength := -1
	for {
		line, err := connection.output.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(name, "Content-Length") {
			contentLength, err = strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if contentLength < 0 {
		t.Fatal("LSP response omitted Content-Length")
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(connection.output, body); err != nil {
		t.Fatal(err)
	}

	return string(body)
}

func (connection *testLSPConnection) shutdown(t *testing.T) {
	t.Helper()
	connection.send(t, `{"jsonrpc":"2.0","id":2,"method":"shutdown","params":null}`)
	connection.receive(t)
	connection.send(t, exitRequest)
	err := <-connection.done
	connection.finished = true
	if err != nil {
		t.Fatal(err)
	}
}

func (connection *testLSPConnection) close(t *testing.T) {
	t.Helper()
	_ = connection.input.Close()
	_ = connection.outputPipe.Close()
	if !connection.finished {
		if err := <-connection.done; err != nil {
			t.Error(err)
		}
	}
}

func TestRunCompletesCurrentDocumentDefinitions(t *testing.T) {
	t.Parallel()
	var input strings.Builder
	for _, body := range []string{
		initializeRequest,
		`{"jsonrpc":"2.0","method":"textDocument/didOpen","params":{"textDocument":{"uri":"file:///main.mcl","text":"let app = \"sample\"\nlet output = ap\n"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"textDocument/completion","params":{"textDocument":{"uri":"file:///main.mcl"},"position":{"line":1,"character":15}}}`,
		exitRequest,
	} {
		fmt.Fprintf(&input, "Content-Length: %d\r\n\r\n%s", len(body), body)
	}
	var output bytes.Buffer
	if err := Run(strings.NewReader(input.String()), &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"label":"app"`) {
		t.Fatalf("completion response omitted the document definition:\n%s", output.String())
	}
}

func TestRuneIndexAtUTF16CountsSupplementaryCharactersAsTwoUnits(t *testing.T) {
	t.Parallel()
	if got := runeIndexAtUTF16([]rune("😀app"), 2); got != 1 {
		t.Fatalf("runeIndexAtUTF16() = %d, want 1", got)
	}
}

func TestRenameTypeDefinitionAndReferences(t *testing.T) {
	t.Parallel()
	const uri = "file:///main.mcl"
	const source = "type Child = Base\ntype Base = String\n"
	s := server{documents: map[string]documentState{uri: {Text: source}}}
	params := renameParams{NewName: renamedTypeName}
	params.TextDocument.URI = uri
	params.Position = location{Line: 0, Character: 13}

	edit := s.rename(params)
	if edit == nil {
		t.Fatal("rename returned no edit")
	}
	edits := edit.Changes[uri]
	if len(edits) != 2 {
		t.Fatalf("rename returned %d edits, want 2: %#v", len(edits), edits)
	}
	for _, expectedLine := range []int{0, 1} {
		found := false
		for _, actual := range edits {
			found = found || actual.Range.Start.Line == expectedLine && actual.NewText == renamedTypeName
		}
		if !found {
			t.Errorf("rename omitted type name on line %d: %#v", expectedLine, edits)
		}
	}
}

func TestRenameExportedTypeRejectsExistingTypeName(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	modulePath := filepath.Join(root, "main.mcl")
	if err := os.WriteFile(filepath.Join(root, "mcl.toml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	const source = "export type Base = String\ntype Renamed = Int\n"
	if err := os.WriteFile(modulePath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	uri := (&url.URL{Scheme: uriSchemeFile, Path: filepath.ToSlash(modulePath)}).String()
	s := server{
		documents:      map[string]documentState{uri: {Text: source}},
		workspaceRoots: []string{root},
	}
	params := renameParams{NewName: renamedTypeName}
	params.TextDocument.URI = uri
	params.Position = location{Line: 0, Character: 14}

	if edit := s.rename(params); edit != nil {
		t.Fatalf("rename returned edits that collide with an existing type: %#v", edit)
	}
}

func TestWatchedFileChangesUpdateTheExportRenameIndex(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for path, source := range map[string]string{
		"mcl.toml":     "",
		"target.mcl":   "export let value = \"old\"\n",
		"importer.mcl": "let local = \"old\"\n",
	} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(root, "target.mcl")
	importer := filepath.Join(root, "importer.mcl")
	importer, err := filepath.EvalSymlinks(importer)
	if err != nil {
		t.Fatal(err)
	}
	index := &engine.DependentMCLSourceIndex{}
	if _, err := index.Sources(target, []string{root}, nil); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	s := server{writer: &output, renameIndex: index, canWatchMCLFiles: true}
	if err := s.handle(request{Method: "initialized"}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "\"method\":\"client/registerCapability\"") || !strings.Contains(output.String(), "\"globPattern\":\"**/*.mcl\"") || !strings.Contains(output.String(), "\"globPattern\":\"**/mcl.toml\"") {
		t.Fatalf("watch registration request is missing: %s", output.String())
	}
	if !s.watchRegistrationPending || s.watchRegistered {
		t.Fatalf("watch registration state = pending:%t registered:%t", s.watchRegistrationPending, s.watchRegistered)
	}
	if err := s.handle(request{ID: jsontext.Value("\"mcl-didChangeWatchedFiles\"")}, nil); err != nil {
		t.Fatal(err)
	}
	if !s.watchRegistered {
		t.Fatal("successful client registration response did not enable watcher updates")
	}

	const importSource = "import \"./target.mcl\" as target\nemit target.value\n"
	if err := os.WriteFile(importer, []byte(importSource), 0o600); err != nil {
		t.Fatal(err)
	}
	importerURI := (&url.URL{Scheme: uriSchemeFile, Path: filepath.ToSlash(importer)}).String()
	params := jsontext.Value(fmt.Appendf(nil, "{\"changes\":[{\"uri\":%q,\"type\":2}]}", importerURI))
	if err := s.handle(request{Method: workspaceWatchedFilesMethod, Params: params}, nil); err != nil {
		t.Fatal(err)
	}
	dependent, err := index.SourcesFromIndex(target, []string{root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := dependent[importer]; got != importSource {
		t.Fatalf("watched update source = %q, want %q", got, importSource)
	}
	markerURI := (&url.URL{Scheme: uriSchemeFile, Path: filepath.ToSlash(filepath.Join(root, "mcl.toml"))}).String()
	markerParams := jsontext.Value(fmt.Appendf(nil, "{\"changes\":[{\"uri\":%q,\"type\":2}]}", markerURI))
	if err := s.handle(request{Method: workspaceWatchedFilesMethod, Params: markerParams}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := index.SourcesFromIndex(target, []string{root}, nil); err != nil {
		t.Fatal(err)
	}
	nestedRoot := filepath.Join(root, "nested")
	if err := os.Mkdir(nestedRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	innerPath := filepath.Join(nestedRoot, "hidden.mcl")
	const innerSource = "import \"../target.mcl\" as target\nemit target.value\n"
	if err := os.WriteFile(innerPath, []byte(innerSource), 0o600); err != nil {
		t.Fatal(err)
	}
	innerPath, err = filepath.EvalSymlinks(innerPath)
	if err != nil {
		t.Fatal(err)
	}
	innerURI := (&url.URL{Scheme: uriSchemeFile, Path: filepath.ToSlash(innerPath)}).String()
	innerParams := jsontext.Value(fmt.Appendf(nil, "{\"changes\":[{\"uri\":%q,\"type\":1}]}", innerURI))
	if err := s.handle(request{Method: workspaceWatchedFilesMethod, Params: innerParams}, nil); err != nil {
		t.Fatal(err)
	}
	dependent, err = index.SourcesFromIndex(target, []string{root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := dependent[innerPath]; !exists {
		t.Fatal("watch update omitted an importer created in the current project")
	}
	if err := os.WriteFile(filepath.Join(nestedRoot, "mcl.toml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	markerURI = (&url.URL{Scheme: uriSchemeFile, Path: filepath.ToSlash(filepath.Join(nestedRoot, "mcl.toml"))}).String()
	markerParams = jsontext.Value(fmt.Appendf(nil, "{\"changes\":[{\"uri\":%q,\"type\":1}]}", markerURI))
	if err := s.handle(request{Method: workspaceWatchedFilesMethod, Params: markerParams}, nil); err != nil {
		t.Fatal(err)
	}
	dependent, err = index.SourcesFromIndex(target, []string{root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := dependent[innerPath]; exists {
		t.Fatal("full snapshot included an MCL file inside a nested project")
	}
}

func TestExportRenameFallsBackToWorkspaceSnapshotWithoutFileWatchSupport(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	const targetSource = "export let value = \"old\"\n"
	const importerSource = "import \"./target.mcl\" as target\nemit target.value\n"
	targetPath := filepath.Join(root, "target.mcl")
	importerPath := filepath.Join(root, "importer.mcl")
	for path, source := range map[string]string{
		filepath.Join(root, "mcl.toml"): "",
		targetPath:                      targetSource,
		importerPath:                    importerSource,
	} {
		if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	targetURI := (&url.URL{Scheme: uriSchemeFile, Path: filepath.ToSlash(targetPath)}).String()
	s := server{
		documents:      map[string]documentState{targetURI: {Text: targetSource}},
		workspaceRoots: []string{root},
	}
	params := renameParams{NewName: "renamed"}
	params.TextDocument.URI = targetURI
	params.Position = location{Line: 0, Character: 13}
	first := s.rename(params)
	if first == nil || len(first.Changes) != 2 {
		t.Fatalf("initial export rename did not include its importer: %#v", first)
	}

	const changedImporterSource = "let local = \"no longer imports target\"\n"
	if err := os.WriteFile(importerPath, []byte(changedImporterSource), 0o600); err != nil {
		t.Fatal(err)
	}
	second := s.rename(params)
	if second == nil {
		t.Fatal("snapshot fallback returned no edit")
	}
	if len(second.Changes) != 1 {
		t.Fatalf("snapshot fallback kept stale importer references: %#v", second.Changes)
	}
}
