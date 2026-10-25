package lsp

import (
	"bufio"
	"context"
	"errors"
	"io"
	"sync"

	"github.com/manifest-configuration-language/mcl/internal/engine"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
)

type server struct {
	protocol.UnimplementedServer
	reader                   *bufio.Reader
	mu                       sync.Mutex
	dispatchMu               sync.Mutex
	dispatchTail             chan struct{}
	documents                map[string]documentState
	workspaceRoots           []string
	renameIndex              *engine.DependentMCLSourceIndex
	canWatchMCLFiles         bool
	watchRegistrationPending bool
	watchRegistered          bool
	watchRegistrationGen     uint64
	stateGeneration          uint64
	diagnosticURIs           map[string]struct{}
	diagnosticCancel         context.CancelFunc
	diagnosticGeneration     uint64
	pendingDiagnosticURIs    map[string]struct{}
	rpc                      jsonrpc2.Conn
	rpcDone                  <-chan struct{}
	watchRegistrationResults chan watchRegistrationResult
	fatalErrors              chan error
	exitRequested            chan struct{}
	diagnosticResults        chan<- diagnosticResult
	notify                   func(string, any) error
	callback                 func(string, any) error
}

type requestSnapshotContextKey struct{}

func isConcurrentReadOnlyMethod(method string) bool {
	switch method {
	case "textDocument/completion", "textDocument/definition", "textDocument/formatting", "textDocument/references", "textDocument/rename":
		return true
	default:
		return false
	}
}

func (s *server) dispatchOrdered(ctx context.Context, method string, handler func(context.Context) (any, error)) (any, error) {
	s.dispatchMu.Lock()
	previous := s.dispatchTail
	next := make(chan struct{})
	s.dispatchTail = next
	s.dispatchMu.Unlock()

	jsonrpc2.Async(ctx)
	<-previous
	if isConcurrentReadOnlyMethod(method) {
		snapshot := s.readOnlySnapshot()
		close(next)
		ctx = context.WithValue(ctx, requestSnapshotContextKey{}, snapshot)

		return handler(ctx)
	}
	defer close(next)

	return handler(ctx)
}

type watchRegistrationResult struct {
	generation uint64
	success    bool
}

func Run(input io.Reader, output io.Writer) error {
	s := &server{
		documents:                make(map[string]documentState),
		diagnosticURIs:           make(map[string]struct{}),
		watchRegistrationResults: make(chan watchRegistrationResult, 1),
		fatalErrors:              make(chan error, 1),
		exitRequested:            make(chan struct{}, 1),
	}
	diagnostics := make(chan diagnosticResult)
	s.diagnosticResults = diagnostics
	s.dispatchTail = make(chan struct{})
	close(s.dispatchTail)
	rpcContext, rpcCancel := context.WithCancel(context.Background())
	s.rpcDone = rpcContext.Done()
	stream := newBoundedStream(input, output)
	s.rpc = jsonrpc2.NewConn(stream, jsonrpc2.WithCodec(protocolCodec{}))
	s.notify = func(method string, params any) error {
		return s.rpc.Notify(rpcContext, method, params)
	}
	s.callback = func(method string, params any) error {
		_, err := s.rpc.Call(rpcContext, method, params, nil)

		return err
	}
	serverHandler := protocol.ServerHandler(s, jsonrpc2.MethodNotFoundHandler)
	orderedHandler := func(ctx context.Context, req *jsonrpc2.Request) (any, error) {
		// Capture read-only state in wire order, then let later document updates proceed.
		return s.dispatchOrdered(ctx, req.Method(), func(ctx context.Context) (any, error) {
			return serverHandler(ctx, req)
		})
	}
	handler := protocol.CancelHandler(orderedHandler)
	s.rpc.Go(rpcContext, func(ctx context.Context, req *jsonrpc2.Request) (any, error) {
		result, err := handler(ctx, req)
		if err != nil {
			if _, ok := errors.AsType[*jsonrpc2.Error](err); !ok {
				select {
				case s.fatalErrors <- err:
				default:
				}

				return nil, jsonrpc2.NewError(jsonrpc2.InternalError, "Internal error")
			}
		}

		return result, err
	})
	stopRPC := func() error {
		return s.rpc.Close()
	}
	defer func() {
		rpcCancel()
		if s.diagnosticCancel != nil {
			s.diagnosticCancel()
		}
	}()

	for {
		select {
		case result := <-diagnostics:
			if err := s.publishDiagnosticResult(result); err != nil {
				if waitErr := stopRPC(); waitErr != nil {
					return errors.Join(err, waitErr)
				}

				return err
			}
		case result := <-s.watchRegistrationResults:
			s.mu.Lock()
			if result.generation == s.watchRegistrationGen && s.watchRegistrationPending {
				s.watchRegistrationPending = false
				if s.watchRegistered != result.success {
					s.stateGeneration++
				}
				s.watchRegistered = result.success
			}
			s.mu.Unlock()
		case err := <-s.fatalErrors:
			if waitErr := stopRPC(); waitErr != nil {
				return errors.Join(err, waitErr)
			}

			return err
		case <-s.exitRequested:
			if err := stopRPC(); err != nil {
				return err
			}

			return nil
		case <-s.rpc.Done():
			err := s.rpc.Err()
			if errors.Is(err, io.EOF) {
				return nil
			}

			return err
		}
	}
}
