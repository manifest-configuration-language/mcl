package lsp

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

func TestReadOnlyHandlerRejectsSnapshotAfterWatchedFileChange(t *testing.T) {
	t.Parallel()

	s := &server{
		documents:         map[string]documentState{"file:///workspace/main.mcl": {Text: "import \"dependency\"", Version: 1}},
		diagnosticResults: make(chan diagnosticResult, 1),
	}
	s.dispatchTail = make(chan struct{})
	close(s.dispatchTail)

	started := make(chan struct{})
	continueHandler := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, err := s.dispatchOrdered(t.Context(), "textDocument/references", func(ctx context.Context) (any, error) {
			snapshot := s.snapshotForRequest(ctx)
			close(started)
			<-continueHandler
			if !s.documentSnapshotIsCurrent(snapshot) {
				return nil, protocol.ErrContentModified
			}

			return nil, nil
		})
		result <- err
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("read-only handler did not start")
	}

	if err := s.DidChangeWatchedFiles(t.Context(), &protocol.DidChangeWatchedFilesParams{
		Changes: []protocol.FileEvent{{URI: uri.URI("file:///workspace/dependency.mcl"), Type: protocol.FileChangeTypeChanged}},
	}); err != nil {
		t.Fatal(err)
	}
	close(continueHandler)

	select {
	case err := <-result:
		if !errors.Is(err, protocol.ErrContentModified) {
			t.Fatalf("read-only handler error = %v, want ErrContentModified", err)
		}
	case <-time.After(time.Second):
		t.Fatal("read-only handler did not finish")
	}
}
