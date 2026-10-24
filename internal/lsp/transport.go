package lsp

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"

	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
)

type protocolCodec struct{}

func (protocolCodec) Marshal(value any) ([]byte, error) {
	switch message := value.(type) {
	case jsonrpc2.RawMessage:
		if message == nil {
			return []byte("null"), nil
		}
		return message, nil
	case *jsonrpc2.RawMessage:
		if message == nil || *message == nil {
			return []byte("null"), nil
		}
		return *message, nil
	default:
		return protocol.Marshal(value)
	}
}

func (protocolCodec) Unmarshal(data []byte, value any) error {
	if message, ok := value.(*jsonrpc2.RawMessage); ok {
		*message = append((*message)[:0], data...)

		return nil
	}

	return protocol.Unmarshal(data, value)
}

type boundedStream struct {
	reader  *bufio.Reader
	output  io.Writer
	closers []io.Closer
	writeMu sync.Mutex
}

func newBoundedStream(input io.Reader, output io.Writer) *boundedStream {
	stream := &boundedStream{reader: bufio.NewReaderSize(input, maximumHeaderSize), output: output}
	if closer, ok := input.(io.Closer); ok {
		stream.closers = append(stream.closers, closer)
	}
	if closer, ok := output.(io.Closer); ok {
		stream.closers = append(stream.closers, closer)
	}

	return stream
}

func (s *boundedStream) Read(ctx context.Context) (jsonrpc2.Message, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	body, frameSize, err := s.readFrame()
	if err != nil {
		return nil, 0, err
	}
	message, err := jsonrpc2.DecodeMessage(body)

	return message, int64(frameSize), err
}

func (s *boundedStream) readFrame() ([]byte, int, error) {
	return readLSPFrame(s.reader)
}

func readLSPFrame(reader *bufio.Reader) ([]byte, int, error) {
	contentLength := -1
	headerSize := 0
	for {
		line, err := reader.ReadSlice('\n')
		if err != nil {
			if errors.Is(err, bufio.ErrBufferFull) {
				return nil, 0, errors.New("header line exceeds maximum size")
			}

			return nil, 0, err
		}
		headerSize += len(line)
		if headerSize > maximumHeaderSize {
			return nil, 0, errors.New("headers exceed maximum size")
		}
		headerLine := strings.TrimRight(string(line), "\r\n")
		if headerLine == "" {
			break
		}
		name, value, ok := strings.Cut(headerLine, ":")
		if ok && strings.EqualFold(name, "Content-Length") {
			if contentLength >= 0 {
				return nil, 0, errors.New("duplicate Content-Length header")
			}
			contentLength, err = strconv.Atoi(strings.TrimSpace(value))
			if err != nil || contentLength < 0 || contentLength > maximumMessageSize {
				return nil, 0, errors.New("invalid Content-Length header")
			}
		}
	}
	if contentLength < 0 {
		return nil, 0, errors.New("missing Content-Length header")
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(reader, body); err != nil {
		return nil, 0, err
	}

	return body, headerSize + contentLength, nil
}

func (s *server) readMessage() ([]byte, error) {
	body, _, err := readLSPFrame(s.reader)

	return body, err
}

func (s *boundedStream) Write(ctx context.Context, message jsonrpc2.Message) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	body, err := jsonrpc2.EncodeMessage(message)
	if err != nil {
		return 0, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	frame := "Content-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n"
	if err := writeFull(s.output, []byte(frame)); err != nil {
		return 0, err
	}
	if err := writeFull(s.output, body); err != nil {
		return 0, err
	}

	return int64(len(frame) + len(body)), nil
}

func writeFull(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}

	return nil
}

func (s *boundedStream) Close() error {
	var firstErr error
	for _, closer := range s.closers {
		if err := closer.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	return firstErr
}
