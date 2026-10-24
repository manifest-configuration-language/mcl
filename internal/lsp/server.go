package lsp

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	"errors"
	"io"
	"sync"

	"github.com/creachadair/jrpc2"
	"github.com/manifest-configuration-language/mcl/internal/engine"
	"go.lsp.dev/jsonrpc2"
)

type server struct {
	reader                   *bufio.Reader
	mu                       sync.Mutex
	documents                map[string]documentState
	workspaceRoots           []string
	renameIndex              *engine.DependentMCLSourceIndex
	canWatchMCLFiles         bool
	watchRegistrationPending bool
	watchRegistered          bool
	watchRegistrationGen     uint64
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
	callback                 func(string, any) (*jrpc2.Response, error)
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
	rpcContext, rpcCancel := context.WithCancel(context.Background())
	s.rpcDone = rpcContext.Done()
	stream := newBoundedStream(input, output)
	s.rpc = jsonrpc2.NewConn(stream)
	s.notify = func(method string, params any) error {
		return s.rpc.Notify(rpcContext, method, params)
	}
	s.callback = func(method string, params any) (*jrpc2.Response, error) {
		_, err := s.rpc.Call(rpcContext, method, params, nil)

		return nil, err
	}
	s.rpc.Go(rpcContext, func(ctx context.Context, req *jsonrpc2.Request) (any, error) {
		result, err := s.handle(req.Method(), jsontext.Value(req.Params()), !req.IsCall(), ctx)
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
