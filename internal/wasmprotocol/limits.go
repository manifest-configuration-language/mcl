// Code generated from cmd/mcl-wasm/protocol.schema.json. DO NOT EDIT.

package wasmprotocol

const RequestOperationNames = "parse, " +
	"lint, " +
	"format, " +
	"check, " +
	"evaluate, " +
	"render, " +
	"completion, " +
	"definition, " +
	"hover, " +
	"references, " +
	"rename, " +
	"renameFileImports"

const (
	MaxSourceBytes          = 65536
	MaxProjectFiles         = 4096
	MaxProjectFileBytes     = 16777216
	MaxProjectBytes         = 67108864
	MaxOutputBytes          = 1048576
	MaxSourceMapBytes       = 1048576
	MaxRenameNameLength     = 256
	EvaluationTimeoutMillis = 4000
	MaxProjectPathLength    = 512
	MaxProjectPathDepth     = 64
)
