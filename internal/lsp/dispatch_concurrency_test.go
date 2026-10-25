package lsp

import (
	"context"
	"testing"
	"time"
)

func TestDispatchOrderedSnapshotsReadOnlyRequestsInWireOrder(t *testing.T) {
	s := &server{documents: map[string]documentState{"file:///main.mcl": {Text: "version one", Version: 1}}}
	s.dispatchTail = make(chan struct{})
	close(s.dispatchTail)

	firstStarted := make(chan int, 1)
	allowFirstFinish := make(chan struct{})
	firstDone := make(chan any, 1)
	go func() {
		result, err := s.dispatchOrdered(context.Background(), "textDocument/completion", func(ctx context.Context) (any, error) {
			snapshot := s.snapshotForRequest(ctx)
			firstStarted <- snapshot.documents["file:///main.mcl"].Version
			<-allowFirstFinish

			return snapshot.documents["file:///main.mcl"].Version, nil
		})
		if err != nil {
			firstDone <- err
			return
		}
		firstDone <- result
	}()

	select {
	case version := <-firstStarted:
		if version != 1 {
			t.Fatalf("first request snapshot version = %d, want 1", version)
		}
	case <-time.After(time.Second):
		t.Fatal("first read-only request did not start")
	}

	changed := make(chan struct{})
	go func() {
		_, err := s.dispatchOrdered(context.Background(), "textDocument/didChange", func(context.Context) (any, error) {
			s.mu.Lock()
			s.documents["file:///main.mcl"] = documentState{Text: "version two", Version: 2}
			s.mu.Unlock()
			close(changed)

			return nil, nil
		})
		if err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-changed:
	case <-time.After(time.Second):
		t.Fatal("document change waited for read-only request completion")
	}

	secondSnapshot := make(chan int, 1)
	secondDone := make(chan struct{})
	go func() {
		_, err := s.dispatchOrdered(context.Background(), "textDocument/definition", func(ctx context.Context) (any, error) {
			secondSnapshot <- s.snapshotForRequest(ctx).documents["file:///main.mcl"].Version
			return nil, nil
		})
		if err != nil {
			t.Error(err)
		}
		close(secondDone)
	}()
	select {
	case version := <-secondSnapshot:
		if version != 2 {
			t.Fatalf("second request snapshot version = %d, want 2", version)
		}
	case <-time.After(time.Second):
		t.Fatal("second read-only request waited for earlier read-only work")
	}
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("second read-only request did not complete while the first was blocked")
	}

	close(allowFirstFinish)
	select {
	case result := <-firstDone:
		if result != 1 {
			t.Fatalf("first request result = %v, want its captured version 1", result)
		}
	case <-time.After(time.Second):
		t.Fatal("first read-only request did not finish")
	}
}
