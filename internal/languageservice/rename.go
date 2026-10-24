package languageservice

import (
	"slices"

	"github.com/manifest-configuration-language/mcl/internal/sourceindex"
	"github.com/manifest-configuration-language/mcl/internal/syntax"
)

// RenameEdit replaces one source range in a project file.
type RenameEdit struct {
	Path    string      `json:"path"`
	Range   SourceRange `json:"range"`
	NewText string      `json:"newText"`
}

// RenameResult contains edits for a symbol declared and referenced in one MCL file.
type RenameResult struct {
	Edits []RenameEdit `json:"edits"`
}

// RenameForSource renames a local or non-exported top-level declaration in one source file.
func RenameForSource(path, source string, position Position, newName string) *RenameResult {
	if !IsMCLIdentifier(newName) {
		return nil
	}
	index, err := AnalyzeSource(source)
	if err != nil {
		return nil
	}
	offset, err := sourceindex.New(source).OffsetAt(sourceindex.Position{Line: position.Line, Character: position.Character})
	if err != nil {
		return nil
	}
	name, selectedSpan, ok := identifierAt(source, offset)
	if !ok || name == newName {
		return nil
	}

	var spans []syntax.SourceSpan
	if local := index.LocalAt(name, offset); local.Found {
		spans = localRenameSpans(source, name, newName, local.Binding, index.Locals, index.References)
		if spans == nil {
			return nil
		}
	} else {
		target, typeName, found := declarationAt(index, name, offset, selectedSpan)
		if !found || ASTDeclarationExported(index.Module.Nodes[target]) || !IsRenameableASTDeclaration(index.Module.Nodes[target]) {
			return nil
		}
		spans = topLevelRenameSpans(source, index, target, name, newName, typeName)
		if spans == nil {
			return nil
		}
	}

	if !candidateParses(source, spans, newName) {
		return nil
	}
	edits := make([]RenameEdit, len(spans))
	for index, span := range spans {
		edits[index] = RenameEdit{Path: path, Range: SourceRangeAt(source, span), NewText: newName}
	}
	slices.SortFunc(edits, func(left, right RenameEdit) int {
		if left.Range.Start.Line != right.Range.Start.Line {
			return left.Range.Start.Line - right.Range.Start.Line
		}

		return left.Range.Start.Character - right.Range.Start.Character
	})

	return &RenameResult{Edits: edits}
}

func declarationAt(index *SemanticIndex, name string, offset int, selected syntax.SourceSpan) (int, bool, bool) {
	for _, reference := range index.References {
		if reference.Name != name || !spanContains(reference.Span, offset) && reference.Span != selected {
			continue
		}
		declaration := index.ResolveTopLevel(name, reference.Span.Start, reference.TypeName)
		if declaration < 0 {
			continue
		}

		return declaration, reference.TypeName, true
	}
	declaration := index.DeclarationAt(name, offset)
	if declaration >= 0 {
		return declaration, IsTypeDeclaration(index.Module.Nodes[declaration]), true
	}

	return -1, false, false
}

func topLevelRenameSpans(source string, index *SemanticIndex, target int, oldName, newName string, typeName bool) []syntax.SourceSpan {
	for declarationIndex, node := range index.Module.Nodes {
		name, _, _, ok := ASTDeclarationInfo(node)
		if !ok {
			continue
		}
		if declarationIndex != target && (name == oldName || name == newName) {
			return nil
		}
	}
	for _, reference := range index.References {
		if reference.TypeName == typeName && reference.Name == newName {
			return nil
		}
	}

	_, _, declarationSpan, ok := ASTDeclarationInfo(index.Module.Nodes[target])
	if !ok {
		return nil
	}
	spans := []syntax.SourceSpan{declarationSpan}
	for _, reference := range index.TopLevelReferences(oldName, target, typeName) {
		if index.LocalAt(newName, reference.Span.Start).Found {
			return nil
		}
		spans = append(spans, syntax.SourceSpan{Start: reference.Span.Start, End: reference.Span.Start + len(oldName)})
	}

	return uniqueValidSpans(source, oldName, spans)
}

func localRenameSpans(source string, oldName, newName string, target ScopedLocalBinding, bindings []ScopedLocalBinding, references []SourceReference) []syntax.SourceSpan {
	for _, binding := range bindings {
		if binding.Name == newName && binding.Declaration != target.Declaration && binding.Scope.Start <= target.Scope.End && target.Scope.Start <= binding.Scope.End {
			return nil
		}
	}
	spans := []syntax.SourceSpan{target.Declaration}
	for _, reference := range references {
		if reference.Name != oldName {
			continue
		}
		resolved := LocalBindingAt(bindings, oldName, reference.Span.Start)
		if !resolved.Found || resolved.Binding.Declaration != target.Declaration {
			continue
		}
		if LocalBindingAt(bindings, newName, reference.Span.Start).Found {
			return nil
		}
		spans = append(spans, syntax.SourceSpan{Start: reference.Span.Start, End: reference.Span.Start + len(oldName)})
	}

	return uniqueValidSpans(source, oldName, spans)
}

func uniqueValidSpans(source, name string, spans []syntax.SourceSpan) []syntax.SourceSpan {
	seen := make(map[syntax.SourceSpan]struct{}, len(spans))
	unique := make([]syntax.SourceSpan, 0, len(spans))
	for _, span := range spans {
		if span.Start < 0 || span.End > len(source) || source[span.Start:span.End] != name {
			return nil
		}
		if _, exists := seen[span]; exists {
			continue
		}
		seen[span] = struct{}{}
		unique = append(unique, span)
	}

	return unique
}

func candidateParses(source string, spans []syntax.SourceSpan, newName string) bool {
	ordered := slices.Clone(spans)
	slices.SortFunc(ordered, func(left, right syntax.SourceSpan) int { return right.Start - left.Start })
	candidate := source
	for _, span := range ordered {
		candidate = candidate[:span.Start] + newName + candidate[span.End:]
	}
	_, err := syntax.ParseAST(candidate)

	return err == nil
}

func identifierAt(source string, offset int) (string, syntax.SourceSpan, bool) {
	if offset < 0 || offset > len(source) {
		return "", syntax.SourceSpan{}, false
	}
	if offset == len(source) || !isIdentifierByte(source[offset]) {
		if offset == 0 || !isIdentifierByte(source[offset-1]) {
			return "", syntax.SourceSpan{}, false
		}
		offset--
	}
	start, end := offset, offset+1
	for start > 0 && isIdentifierByte(source[start-1]) {
		start--
	}
	for end < len(source) && isIdentifierByte(source[end]) {
		end++
	}
	name := source[start:end]
	if !IsMCLIdentifier(name) {
		return "", syntax.SourceSpan{}, false
	}

	return name, syntax.SourceSpan{Start: start, End: end}, true
}

func isIdentifierByte(value byte) bool {
	return value == '_' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}
