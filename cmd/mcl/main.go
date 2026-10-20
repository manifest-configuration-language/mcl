package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/manifest-configuration-language/mcl/internal/engine"
	"github.com/manifest-configuration-language/mcl/internal/lsp"
	"github.com/manifest-configuration-language/mcl/internal/sourceindex"
	"github.com/manifest-configuration-language/mcl/internal/syntax"
	"github.com/manifest-configuration-language/mcl/pkg/mcl"
	"github.com/rivo/uniseg"
	"github.com/spf13/pflag"
)

// 診断元のパスが不明なときに表示する文字列。
const unknownPath = "<unknown>"

type commandDefinition struct {
	name        string
	description string
	usage       string
	flags       string
	run         func(context.Context, []string) error
}

var commandDefinitions = []commandDefinition{
	{name: "render", description: "Render Kubernetes resources", usage: "Usage: mcl render [flags] <file>", flags: "  --phase <phase>             Render one lifecycle phase\n  --operation <operation>     Render one operation\n  --output-dir <directory>    Write operation outputs to a directory\n  --format <yaml|json>        Select output format (default: yaml)\n\n--operation and --phase cannot be combined. --operation requires --output-dir.", run: renderCommand},
	{name: "check", description: "Validate rendered resources", usage: "Usage: mcl check [flags] <file>", flags: "  --operation <operation>    Validate one operation", run: checkCommand},
	{name: "explain", description: "Explain a resource field and its source", usage: "Usage: mcl explain [flags] <file>", flags: "  --api-version <version>    Kubernetes API version (required)\n  --kind <kind>              Kubernetes resource kind (required)\n  --name <name>              Kubernetes resource name (required)\n  --namespace <namespace>    Kubernetes namespace\n  --phase <phase>            Lifecycle phase (default: main)\n  --operation <operation>    Evaluate one operation\n  --path <json-pointer>      JSON Pointer within the resource", run: explainCommand},
	{name: "diff", description: "Compare rendered resources", usage: "Usage: mcl diff [flags] <before-file> <after-file>", flags: "  --operation <operation>    Compare one operation", run: diffCommand},
	{name: "lint", description: "Report source diagnostics", usage: "Usage: mcl lint [--deny-warnings] <file...>", flags: "  --deny-warnings               Return an error when warnings are found", run: func(_ context.Context, args []string) error { return lintCommand(args) }},
	{name: "fmt", description: "Format an MCL source file", usage: "Usage: mcl fmt [flags] <file>", flags: "  -w       Replace the input file with formatted source\n  --check  Return an error when source is not formatted", run: func(_ context.Context, args []string) error { return formatCommand(args) }},
	{name: "update", description: "Update external dependencies and lock data", usage: "Usage: mcl update [file]", run: updateCommand},
	{name: "lsp", description: "Start the Language Server Protocol server", usage: "Usage: mcl lsp", run: runLSP},
	{name: "version", description: "Print build version information", usage: "Usage: mcl version", run: runVersion},
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		diagnostics := sourceDiagnostics(err)
		if len(diagnostics) == 0 {
			fmt.Fprintln(os.Stderr, "mcl:", err)
		}
		sources := make(map[string]indexedSource)
		for _, diagnostic := range diagnostics {
			printSourceDiagnostic(diagnostic, sources)
		}
		fmt.Fprintln(os.Stderr, "Run 'mcl help' to see available commands and flags.")
		os.Exit(1)
	}
}

func sourceDiagnostics(err error) []mcl.SourceDiagnosticError {
	if err == nil {
		return nil
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var diagnostics []mcl.SourceDiagnosticError
		for _, child := range joined.Unwrap() {
			diagnostics = append(diagnostics, sourceDiagnostics(child)...)
		}

		return diagnostics
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return sourceDiagnostics(wrapped.Unwrap())
	}
	if diagnostic, ok := errors.AsType[mcl.SourceDiagnosticError](err); ok {
		return []mcl.SourceDiagnosticError{diagnostic}
	}
	if diagnostic, ok := mcl.SourceDiagnosticFromError(err); ok {
		return []mcl.SourceDiagnosticError{diagnostic}
	}

	return nil
}

func sourceDiagnosticWithPath(err error, path string) error {
	diagnostic, ok := mcl.SourceDiagnosticFromError(err)
	if !ok || diagnostic.Path != "" {
		return err
	}
	diagnostic.Path = path

	return diagnostic
}

type indexedSource struct {
	content []byte
	index   *sourceindex.Index
}

func printSourceDiagnostic(diagnostic mcl.SourceDiagnosticError, sources map[string]indexedSource) {
	path := diagnostic.Path
	if path == "" {
		path = unknownPath
	}
	location := fmt.Sprintf("%s: %d", path, diagnostic.Span.Start)
	source, err := indexedDiagnosticSource(diagnostic.Path, sources)
	if err == nil && diagnostic.Span.Start >= 0 {
		line, column, lineText := sourceLineAtIndex(source.index, source.content, diagnostic.Span.Start)
		location = fmt.Sprintf("%s:%d:%d", path, line, column)
		fmt.Fprintf(os.Stderr, "  %s: %s: %s\n", location, diagnostic.Code, diagnostic.DisplayMessage())
		printSourceLine(line, column, lineText)
	} else {
		fmt.Fprintf(os.Stderr, "  %s: %s: %s\n", location, diagnostic.Code, diagnostic.DisplayMessage())
	}
	for _, related := range diagnostic.Related {
		relatedPath := related.Path
		if relatedPath == "" {
			relatedPath = unknownPath
		}
		relatedLocation := fmt.Sprintf("%s: %d", relatedPath, related.Span.Start)
		relatedSource, err := indexedDiagnosticSource(related.Path, sources)
		if err == nil && related.Span.Start >= 0 {
			line, column, lineText := sourceLineAtIndex(relatedSource.index, relatedSource.content, related.Span.Start)
			relatedLocation = fmt.Sprintf("%s:%d:%d", relatedPath, line, column)
			fmt.Fprintf(os.Stderr, "  related: %s: %s\n", relatedLocation, related.Message)
			printSourceLine(line, column, lineText)
		} else {
			fmt.Fprintf(os.Stderr, "  related: %s: %s\n", relatedLocation, related.Message)
		}
	}
}

func indexedDiagnosticSource(path string, cache map[string]indexedSource) (indexedSource, error) {
	if source, exists := cache[path]; exists {
		return source, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return indexedSource{}, err
	}
	source := indexedSource{content: content, index: sourceindex.New(string(content))}
	cache[path] = source

	return source, nil
}

func sourceLineAt(source []byte, offset int) (int, int, string) {
	index := sourceindex.New(string(source))

	return sourceLineAtIndex(index, source, offset)
}

func sourceLineAtIndex(index *sourceindex.Index, source []byte, offset int) (int, int, string) {
	line, lineStart, lineEnd := index.LineAt(offset)
	lineText := source[lineStart:lineEnd]
	prefixWidth := len(fmt.Sprintf("    %d | ", line))
	column := terminalColumn(lineText[:min(offset-lineStart, len(lineText))], prefixWidth) - prefixWidth + 1

	return line, column, string(lineText)
}

func printSourceLine(line, column int, text string) {
	prefix := fmt.Sprintf("    %d | ", line)
	fmt.Fprintf(os.Stderr, "%s%s\n", prefix, text)
	fmt.Fprintf(os.Stderr, "%s%s^\n", strings.Repeat(" ", len(prefix)-2)+"| ", strings.Repeat(" ", max(column-1, 0)))
}

func terminalColumn(text []byte, initialColumn int) int {
	column := initialColumn
	for len(text) > 0 {
		tab := bytes.IndexByte(text, '\t')
		if tab < 0 {
			return column + uniseg.StringWidth(string(text))
		}
		column += uniseg.StringWidth(string(text[:tab]))
		column += 8 - column%8
		text = text[tab+1:]
	}

	return column
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usageError()
	}
	command := args[0]
	if command == "help" || command == "--help" || command == "-h" {
		if command == "help" && len(args) == 2 {
			printHelp(args[1])
		} else {
			printHelp("")
		}

		return nil
	}
	for _, argument := range args[1:] {
		if argument == "--help" || argument == "-h" {
			printHelp(command)

			return nil
		}
	}
	if command == "--version" || command == "-version" {
		command = "version"
	}
	definition, ok := commandDefinitionByName(command)
	if !ok {
		return usageError()
	}

	return definition.run(ctx, args[1:])
}

func commandDefinitionByName(name string) (commandDefinition, bool) {
	for _, definition := range commandDefinitions {
		if definition.name == name {
			return definition, true
		}
	}

	return commandDefinition{}, false
}

func updateCommand(ctx context.Context, args []string) error {
	if len(args) > 1 {
		return errors.New("update accepts at most one input file")
	}
	path := "main.mcl"
	if len(args) == 1 {
		path = args[0]
	}

	return mcl.UpdateFileContext(ctx, path)
}

func runLSP(_ context.Context, args []string) error {
	if len(args) != 0 {
		return errors.New("lsp does not accept arguments")
	}

	//nolint:contextcheck // LSPの標準入出力ループはプロセス終了まで動作します。
	return lsp.Run(os.Stdin, os.Stdout)
}

func runVersion(_ context.Context, args []string) error {
	if len(args) != 0 {
		return errors.New("version does not accept arguments")
	}
	identity := mcl.CurrentBuildIdentity()
	revision := identity.Revision
	if revision == "" {
		revision = "unknown"
	}
	modified := identity.Modified
	if modified == "" {
		modified = "unknown"
	}
	fmt.Fprintf(os.Stdout, "mcl %s (revision %s, modified %s)\n", mcl.ToolchainVersion, revision, modified)

	return nil
}

func lintCommand(args []string) error {
	flags := pflag.NewFlagSet("lint", pflag.ContinueOnError)
	denyWarnings := flags.Bool("deny-warnings", false, "return an error when warnings are found")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() == 0 {
		return errors.New("lint requires at least one input file")
	}
	var warnings int
	for _, path := range flags.Args() {
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		module, err := syntax.ParseAST(string(source))
		if err != nil {
			return sourceDiagnosticWithPath(err, path)
		}
		diagnostics := engine.LintAST(module)
		warnings += len(diagnostics)
		index := sourceindex.New(string(source))
		for _, diagnostic := range diagnostics {
			printLintDiagnostic(path, source, index, diagnostic)
		}
	}
	if *denyWarnings && warnings > 0 {
		return fmt.Errorf("lint found %d warning(s)", warnings)
	}

	return nil
}

func printLintDiagnostic(path string, source []byte, index *sourceindex.Index, diagnostic mcl.Diagnostic) {
	if diagnostic.Span.Start < 0 {
		fmt.Fprintf(os.Stderr, "%s: %d: warning [%s]: %s\n", path, diagnostic.Line, diagnostic.Code, diagnostic.Message)

		return
	}
	line, column, lineText := sourceLineAtIndex(index, source, diagnostic.Span.Start)
	fmt.Fprintf(os.Stderr, "%s:%d:%d: warning [%s]: %s\n", path, line, column, diagnostic.Code, diagnostic.Message)
	printSourceLine(line, column, lineText)
}

func diffCommand(ctx context.Context, args []string) error {
	flags := pflag.NewFlagSet("diff", pflag.ContinueOnError)
	operation := flags.String("operation", "", "operation to compare")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 2 {
		return errors.New("diff requires before and after input files")
	}
	beforePath, afterPath := flags.Arg(0), flags.Arg(1)
	beforeCatalog, err := mcl.LoadKubernetesResourceCatalogForFile(beforePath)
	if err != nil {
		return err
	}
	afterCatalog, err := mcl.LoadKubernetesResourceCatalogForFile(afterPath)
	if err != nil {
		return err
	}
	evaluate := func(path string) (mcl.Bundle, error) {
		if *operation != "" {
			return mcl.EvaluateFileForOperationContext(ctx, path, *operation)
		}

		return mcl.EvaluateFileContext(ctx, path)
	}
	before, err := evaluate(beforePath)
	if err != nil {
		return fmt.Errorf("evaluate before input: %w", err)
	}
	if err := mcl.ValidateBundleOutputWithCatalog(before, beforeCatalog); err != nil {
		return err
	}
	after, err := evaluate(afterPath)
	if err != nil {
		return fmt.Errorf("evaluate after input: %w", err)
	}
	if err := mcl.ValidateBundleOutputWithCatalog(after, afterCatalog); err != nil {
		return err
	}
	changes, err := mcl.DiffBundlesWithCatalogs(before, after, beforeCatalog, afterCatalog)
	if err != nil {
		return err
	}
	for _, change := range changes {
		switch change.Kind {
		case mcl.BundleResourceAdded:
			fmt.Fprintf(os.Stdout, "resource added: %s\n", change.Resource)
		case mcl.BundleResourceRemoved:
			fmt.Fprintf(os.Stdout, "resource removed: %s\n", change.Resource)
		case mcl.BundleFieldAdded:
			fmt.Fprintf(os.Stdout, "field added: %s%s = %s\n", change.Resource, change.Path, change.After)
		case mcl.BundleFieldRemoved:
			fmt.Fprintf(os.Stdout, "field removed: %s%s = %s\n", change.Resource, change.Path, change.Before)
		case mcl.BundleFieldChanged:
			fmt.Fprintf(os.Stdout, "field changed: %s%s: %s -> %s\n", change.Resource, change.Path, change.Before, change.After)
		case mcl.BundleLifecycleChanged:
			fmt.Fprintf(os.Stdout, "lifecycle changed: %s: %s -> %s\n", change.Resource, change.Before, change.After)
		}
	}

	return nil
}

func checkCommand(ctx context.Context, args []string) error {
	flags := pflag.NewFlagSet("check", pflag.ContinueOnError)
	operation := flags.String("operation", "", "operation to check")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("check requires one input file")
	}
	catalog, err := mcl.LoadKubernetesResourceCatalogForFile(flags.Arg(0))
	if err != nil {
		return err
	}

	var bundle mcl.Bundle
	if *operation != "" {
		bundle, err = mcl.EvaluateFileForOperationContext(ctx, flags.Arg(0), *operation)
	} else {
		bundle, err = mcl.EvaluateFileContext(ctx, flags.Arg(0))
	}
	if err != nil {
		return err
	}

	return mcl.ValidateBundleOutputWithCatalog(bundle, catalog)
}

func renderCommand(ctx context.Context, args []string) error {
	flags := pflag.NewFlagSet("render", pflag.ContinueOnError)
	phase := flags.String("phase", "", "lifecycle phase to render")
	operation := flags.String("operation", "", "operation to render")
	outputDir := flags.String("output-dir", "", "directory for operation output files")
	format := flags.String("format", "yaml", "output format (yaml or json)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("render requires one input file")
	}
	if *operation != "" && *phase != "" {
		return errors.New("--operation and --phase cannot be used together")
	}
	if *operation != "" && *outputDir == "" {
		return errors.New("--operation requires --output-dir")
	}
	if *operation == "" && *outputDir != "" {
		return errors.New("--output-dir requires --operation")
	}
	if *format != "yaml" && *format != "json" {
		return fmt.Errorf("unsupported output format %q", *format)
	}
	catalog, err := mcl.LoadKubernetesResourceCatalogForFile(flags.Arg(0))
	if err != nil {
		return err
	}
	var bundle mcl.Bundle
	if *operation != "" {
		bundle, err = mcl.EvaluateFileForOperationContext(ctx, flags.Arg(0), *operation)
	} else {
		bundle, err = mcl.EvaluateFileContext(ctx, flags.Arg(0))
	}
	if err != nil {
		return err
	}
	plan, err := engine.NewBundleOutputPlanWithCatalog(bundle, catalog)
	if err != nil {
		return err
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	if *operation != "" {
		files, err := plan.RenderOperationWithFormat(*operation, *format)
		if err != nil {
			return err
		}

		return writeOperationFiles(*outputDir, files)
	}
	if *phase == "" {
		*phase = "main"
	}
	var output []byte
	if *format == "json" {
		output, err = plan.RenderPhaseJSON(*phase)
	} else {
		output, err = plan.RenderPhase(*phase)
	}
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(output)

	return err
}

func writeOperationFiles(outputDir string, files []mcl.RenderedFile) error {
	absoluteDir, err := filepath.Abs(outputDir)
	if err != nil {
		return fmt.Errorf("resolve operation output directory: %w", err)
	}
	if info, err := os.Lstat(absoluteDir); err == nil && info.Mode()&os.ModeSymlink != 0 {
		absoluteDir, err = filepath.EvalSymlinks(absoluteDir)
		if err != nil {
			return fmt.Errorf("resolve operation output directory symlink: %w", err)
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect operation output directory: %w", err)
	}
	if filepath.Base(absoluteDir) == string(filepath.Separator) || absoluteDir == filepath.VolumeName(absoluteDir)+string(filepath.Separator) {
		return errors.New("operation output directory cannot be a filesystem root")
	}

	parentDir := filepath.Dir(absoluteDir)
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		return fmt.Errorf("create operation output parent directory: %w", err)
	}
	stageDir, err := os.MkdirTemp(parentDir, "."+filepath.Base(absoluteDir)+".mcl-")
	if err != nil {
		return fmt.Errorf("create temporary operation output directory: %w", err)
	}
	defer os.RemoveAll(stageDir)
	if err := os.Chmod(stageDir, 0o700); err != nil {
		return fmt.Errorf("set temporary operation output directory permissions: %w", err)
	}

	manifest := operationOutputManifest{}
	seen := make(map[string]struct{}, len(files))
	for _, file := range files {
		if file.Name == "" || filepath.Base(file.Name) != file.Name || file.Name == "." || file.Name == ".." || file.Name == operationOutputManifestName {
			return fmt.Errorf("invalid operation output filename %q", file.Name)
		}
		if _, ok := seen[file.Name]; ok {
			return fmt.Errorf("duplicate operation output filename %q", file.Name)
		}
		seen[file.Name] = struct{}{}
		if err := os.WriteFile(filepath.Join(stageDir, file.Name), file.Content, 0o600); err != nil {
			return fmt.Errorf("write operation output file %q: %w", file.Name, err)
		}
		digest := sha256.Sum256(file.Content)
		manifest.Files = append(manifest.Files, operationOutputManifestEntry{Name: file.Name, SHA256: hex.EncodeToString(digest[:])})
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("encode operation output manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, operationOutputManifestName), manifestBytes, 0o600); err != nil {
		return fmt.Errorf("write operation output manifest: %w", err)
	}

	backupDir := stageDir + ".previous"
	if _, err := os.Lstat(backupDir); err == nil {
		return errors.New("temporary operation output backup path already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect temporary operation output backup path: %w", err)
	}

	if _, err := os.Lstat(absoluteDir); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect operation output directory: %w", err)
		}
		if err := os.Rename(stageDir, absoluteDir); err != nil {
			return fmt.Errorf("install operation output directory: %w", err)
		}

		return nil
	}
	info, err := os.Stat(absoluteDir)
	if err != nil {
		return fmt.Errorf("inspect operation output directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("operation output path %q is not a directory", outputDir)
	}
	if err := validateManagedOperationOutputDirectory(absoluteDir); err != nil {
		return err
	}
	if err := os.Rename(absoluteDir, backupDir); err != nil {
		return fmt.Errorf("move previous operation output directory: %w", err)
	}
	if err := os.Rename(stageDir, absoluteDir); err != nil {
		installErr := fmt.Errorf("install operation output directory: %w", err)
		if restoreErr := os.Rename(backupDir, absoluteDir); restoreErr != nil {
			return errors.Join(installErr, fmt.Errorf("restore previous operation output directory: %w", restoreErr))
		}

		return installErr
	}
	if err := os.RemoveAll(backupDir); err != nil {
		return fmt.Errorf("remove previous operation output directory %q after installing new output: %w", backupDir, err)
	}

	return nil
}

const operationOutputManifestName = ".mcl-manifest.json"

type operationOutputManifest struct {
	Files []operationOutputManifestEntry `json:"files"`
}

type operationOutputManifestEntry struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

func validateManagedOperationOutputDirectory(directory string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("read operation output directory: %w", err)
	}
	if len(entries) == 0 {
		return nil
	}
	manifestPath := filepath.Join(directory, operationOutputManifestName)
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("refusing to replace non-empty operation output directory without %s", operationOutputManifestName)
		}

		return fmt.Errorf("inspect operation output manifest: %w", err)
	}
	if !manifestInfo.Mode().IsRegular() {
		return errors.New("operation output manifest is not a regular file")
	}
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read operation output manifest: %w", err)
	}
	var manifest operationOutputManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return fmt.Errorf("decode operation output manifest: %w", err)
	}

	managed := map[string]string{operationOutputManifestName: ""}
	for _, file := range manifest.Files {
		if file.Name == "" || filepath.Base(file.Name) != file.Name || file.Name == "." || file.Name == ".." || file.Name == operationOutputManifestName || len(file.SHA256) != sha256.Size*2 {
			return fmt.Errorf("invalid operation output manifest entry %q", file.Name)
		}
		if _, exists := managed[file.Name]; exists {
			return fmt.Errorf("duplicate operation output manifest entry %q", file.Name)
		}
		managed[file.Name] = file.SHA256
	}
	if len(entries) != len(managed) {
		return errors.New("refusing to replace operation output directory containing unmanaged files")
	}
	for _, entry := range entries {
		wantDigest, exists := managed[entry.Name()]
		if !exists {
			return fmt.Errorf("refusing to replace operation output directory containing unmanaged file %q", entry.Name())
		}
		if entry.Name() == operationOutputManifestName {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspect managed operation output file %q: %w", entry.Name(), err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("managed operation output file %q is not a regular file", entry.Name())
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read managed operation output file %q: %w", entry.Name(), err)
		}
		digest := sha256.Sum256(content)
		if hex.EncodeToString(digest[:]) != wantDigest {
			return fmt.Errorf("refusing to replace modified operation output file %q", entry.Name())
		}
	}

	return nil
}

func formatCommand(args []string) error {
	flags := pflag.NewFlagSet("fmt", pflag.ContinueOnError)
	write := flags.BoolP("write", "w", false, "write formatted source to the input file")
	check := flags.Bool("check", false, "return an error when source is not formatted")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("fmt requires one input file")
	}
	path := flags.Arg(0)
	source, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	formatted, err := syntax.FormatSource(string(source))
	if err != nil {
		return sourceDiagnosticWithPath(err, path)
	}
	formattedBytes := []byte(formatted)
	if *write {
		if err := writeFileAtomically(path, formattedBytes); err != nil {
			return fmt.Errorf("write formatted source: %w", err)
		}

		return nil
	}
	if *check {
		if !bytes.Equal(source, formattedBytes) {
			return fmt.Errorf("%s is not formatted", path)
		}

		return nil
	}
	_, err = os.Stdout.Write(formattedBytes)

	return err
}

func writeFileAtomically(path string, content []byte) error {
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolve input file: %w", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("inspect input file: %w", err)
	}
	directory := filepath.Dir(target)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(target)+".mcl-*")
	if err != nil {
		return fmt.Errorf("create temporary formatted file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(info.Mode().Perm()); err != nil {
		_ = temporary.Close()

		return fmt.Errorf("set formatted file permissions: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()

		return fmt.Errorf("write temporary formatted file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()

		return fmt.Errorf("sync temporary formatted file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary formatted file: %w", err)
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		return fmt.Errorf("replace input file: %w", err)
	}

	return nil
}

func usageError() error {
	return errors.New("invalid command or arguments")
}

func printHelp(command string) {
	if command == "" {
		printGeneralHelp()

		return
	}
	definition, ok := commandDefinitionByName(command)
	if !ok {
		fmt.Fprintf(os.Stdout, "Unknown command %q.\n\n", command)
		printGeneralHelp()

		return
	}
	fmt.Fprintln(os.Stdout, definition.usage)
	if definition.flags != "" {
		fmt.Fprintf(os.Stdout, "\nFlags:\n%s\n", definition.flags)
	}
}

func printGeneralHelp() {
	fmt.Fprintln(os.Stdout, "Usage: mcl <command> [flags] [file]\n\nCommands:")
	for _, definition := range commandDefinitions {
		fmt.Fprintf(os.Stdout, "  %-8s %s\n", definition.name, definition.description)
	}
	fmt.Fprintln(os.Stdout, "  help     Show this help\n\nRun 'mcl <command> --help' for command-specific flag details.")
}
