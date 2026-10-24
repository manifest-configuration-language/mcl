package lsp

import (
	"github.com/manifest-configuration-language/mcl/internal/languageservice"
	"github.com/manifest-configuration-language/mcl/internal/syntax"
)

type moduleSemanticIndex = languageservice.SemanticIndex

func analyzeSource(source string) (*moduleSemanticIndex, error) {
	return languageservice.AnalyzeSource(source)
}
func analyzeModule(module syntax.ModuleAST) *moduleSemanticIndex {
	return languageservice.AnalyzeModule(module)
}

type sourceReference = languageservice.SourceReference
type qualifiedReference = languageservice.QualifiedReference
type scopedLocalBinding = languageservice.ScopedLocalBinding
type localBindingMatch = languageservice.LocalBindingMatch
type localBinding = languageservice.LocalBinding

func astDeclarationInfo(node syntax.ASTNode) (string, int, syntax.SourceSpan, bool) {
	return languageservice.ASTDeclarationInfo(node)
}
func astDeclarationExported(node syntax.ASTNode) bool {
	return languageservice.ASTDeclarationExported(node)
}
func isRenameableASTDeclaration(node syntax.ASTNode) bool {
	return languageservice.IsRenameableASTDeclaration(node)
}
func isTypeDeclaration(node syntax.ASTNode) bool { return languageservice.IsTypeDeclaration(node) }
func isMCLIdentifier(value string) bool          { return languageservice.IsMCLIdentifier(value) }
func localBindingAt(bindings []scopedLocalBinding, name string, offset int) localBindingMatch {
	return languageservice.LocalBindingAt(bindings, name, offset)
}
func declaredName(line string) (string, int, bool) { return languageservice.DeclaredName(line) }
func utf16Length(runes []rune) int                 { return languageservice.UTF16Length(runes) }
func runeIndexAtUTF16(runes []rune, character int) int {
	return languageservice.RuneIndexAtUTF16(runes, character)
}
func isIdentifierRune(character rune) bool { return languageservice.IsIdentifierRune(character) }
func partialLocalBindings(source string, offset int) []localBinding {
	return languageservice.PartialLocalBindings(source, offset)
}
