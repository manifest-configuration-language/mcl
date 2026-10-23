package lsp

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	"errors"
	"io"
	"strconv"
	"sync"

	"github.com/creachadair/jrpc2"
	"github.com/creachadair/jrpc2/channel"
	"github.com/manifest-configuration-language/mcl/internal/engine"
)

type server struct {
	reader                   *bufio.Reader
	writeMu                  sync.Mutex
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
	rpc                      *jrpc2.Server
	rpcDone                  <-chan struct{}
	watchRegistrationResults chan watchRegistrationResult
	fatalErrors              chan error
	exitRequested            chan struct{}
	diagnosticResults        chan<- diagnosticResult
	notify                   func(string, any) error
	callback                 func(string, any) (*jrpc2.Response, error)
	rpcChannel               *lspChannel
}

type watchRegistrationResult struct {
	generation uint64
	success    bool
}

func (s *server) Assign(ctx context.Context, _ string) jrpc2.Handler {
	frame := s.rpcChannel.assign(jrpc2.InboundRequest(ctx).IsNotification())

	return func(ctx context.Context, req *jrpc2.Request) (any, error) {
		defer s.rpcChannel.finish(frame)
		result, err := s.handle(req.Method(), jsontext.Value(req.ParamString()), req.IsNotification(), ctx)
		if err != nil {
			if _, ok := errors.AsType[*jrpc2.Error](err); !ok {
				select {
				case s.fatalErrors <- err:
				default:
				}

				return nil, &jrpc2.Error{Code: jrpc2.InternalError, Message: "Internal error"}
			}
		}

		return result, err
	}
}

// lspChannel keeps the protocol's frame limits outside the JSON-RPC library.
type lspChannel struct {
	server  *server
	input   io.Reader
	output  io.Writer
	closers []io.Closer
	frameMu sync.Mutex
	frame   *inboundFrame
}

type inboundFrame struct {
	done            chan struct{}
	pendingHandlers int
	hasResponse     bool
	completed       bool
}

func newLSPChannel(s *server, input io.Reader, output io.Writer) *lspChannel {
	ch := &lspChannel{server: s, input: input, output: output}
	if closer, ok := input.(io.Closer); ok {
		ch.closers = append(ch.closers, closer)
	}
	if closer, ok := output.(io.Closer); ok {
		ch.closers = append(ch.closers, closer)
	}

	return ch
}

func (ch *lspChannel) Send(body []byte) error {
	ch.server.writeMu.Lock()
	defer ch.server.writeMu.Unlock()

	if _, err := io.WriteString(ch.output, "Content-Length: "+strconv.Itoa(len(body))+"\r\n\r\n"); err != nil {
		return err
	}
	_, err := ch.output.Write(body)
	ch.frameMu.Lock()
	if frame := ch.frame; frame != nil && frame.pendingHandlers == 0 {
		ch.complete(frame)
	}
	ch.frameMu.Unlock()

	return err
}

func (ch *lspChannel) Recv() ([]byte, error) {
	ch.frameMu.Lock()
	previous := ch.frame
	ch.frameMu.Unlock()
	if previous != nil {
		<-previous.done
	}
	body, err := ch.server.readMessage()
	if err == nil {
		ch.frameMu.Lock()
		ch.frame = &inboundFrame{done: make(chan struct{})}
		ch.frameMu.Unlock()
	}

	return body, err
}

func (ch *lspChannel) assign(notification bool) *inboundFrame {
	ch.frameMu.Lock()
	defer ch.frameMu.Unlock()
	if ch.frame == nil {
		ch.frame = &inboundFrame{done: make(chan struct{})}
	}
	ch.frame.pendingHandlers++
	ch.frame.hasResponse = ch.frame.hasResponse || !notification

	return ch.frame
}

func (ch *lspChannel) finish(frame *inboundFrame) {
	ch.frameMu.Lock()
	defer ch.frameMu.Unlock()
	frame.pendingHandlers--
	if frame.pendingHandlers == 0 && !frame.hasResponse {
		ch.complete(frame)
	}
}

func (ch *lspChannel) complete(frame *inboundFrame) {
	if !frame.completed {
		frame.completed = true
		close(frame.done)
	}
}

func (ch *lspChannel) completeCurrent() {
	ch.frameMu.Lock()
	if ch.frame != nil {
		ch.complete(ch.frame)
	}
	ch.frameMu.Unlock()
}

func (ch *lspChannel) Close() error {
	var firstErr error
	for _, closer := range ch.closers {
		if err := closer.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	return firstErr
}

var _ channel.Channel = (*lspChannel)(nil)

func Run(input io.Reader, output io.Writer) error {
	s := &server{
		reader:                   bufio.NewReaderSize(input, maximumHeaderSize),
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
	s.rpc = jrpc2.NewServer(s, &jrpc2.ServerOptions{Concurrency: 1, AllowPush: true, DisableBuiltin: true})
	s.rpcChannel = newLSPChannel(s, input, output)
	s.notify = func(method string, params any) error {
		return s.rpc.Notify(rpcContext, method, params)
	}
	s.callback = func(method string, params any) (*jrpc2.Response, error) {
		response, err := s.rpc.Callback(rpcContext, method, params)
		s.rpcChannel.completeCurrent()

		return response, err
	}
	s.rpc.Start(s.rpcChannel)
	finished := make(chan error, 1)
	go func() { finished <- s.rpc.Wait() }()
	stopRPC := func() error {
		s.rpc.Stop()

		return <-finished
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
		case err := <-finished:
			if errors.Is(err, io.EOF) || errors.Is(err, jrpc2.ErrConnClosed) {
				return nil
			}

			return err
		}
	}
}
