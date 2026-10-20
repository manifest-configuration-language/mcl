package lsp

import (
	"bufio"
	"context"
	"io"

	"github.com/manifest-configuration-language/mcl/internal/engine"
)

type server struct {
	reader                   *bufio.Reader
	writer                   io.Writer
	documents                map[string]documentState
	workspaceRoots           []string
	renameIndex              *engine.DependentMCLSourceIndex
	canWatchMCLFiles         bool
	watchRegistrationPending bool
	watchRegistered          bool
	diagnosticURIs           map[string]struct{}
	diagnosticCancel         context.CancelFunc
	diagnosticGeneration     uint64
	pendingDiagnosticURIs    map[string]struct{}
}
