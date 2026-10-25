package mcl

import (
	"context"

	"github.com/manifest-configuration-language/mcl/internal/engine"
)

type BundleChangeKind = engine.BundleChangeKind

const BundleResourceAdded = engine.BundleResourceAdded

const BundleResourceRemoved = engine.BundleResourceRemoved

const BundleFieldAdded = engine.BundleFieldAdded

const BundleFieldRemoved = engine.BundleFieldRemoved

const BundleFieldChanged = engine.BundleFieldChanged

const BundleLifecycleChanged = engine.BundleLifecycleChanged

type BundleChange = engine.BundleChange

// DiffBundlesはResource、フィールド、Lifecycleの差分をBundle順に返します。
func DiffBundles(before, after Bundle) ([]BundleChange, error) {
	return engine.DiffBundles(before, after)
}

// DiffBundlesWithCatalogsは各BundleのCatalogを使ってResource identityを解決し、差分を返します。
func DiffBundlesWithCatalogs(before, after Bundle, beforeCatalog, afterCatalog KubernetesResourceCatalog) ([]BundleChange, error) {
	return engine.DiffBundlesWithCatalogs(before, after, beforeCatalog, afterCatalog)
}

type Bundle = engine.Bundle

type Artifact = engine.Artifact

type Lifecycle = engine.Lifecycle

type Diagnostic = engine.Diagnostic
type DiagnosticAction = engine.DiagnosticAction
type DiagnosticTextEdit = engine.DiagnosticTextEdit

const DiagnosticActionRemoveUnreachableDefinition = engine.DiagnosticActionRemoveUnreachableDefinition

const KubernetesResourceCatalogRevision = engine.KubernetesResourceCatalogRevision

type KubernetesResourceScope = engine.KubernetesResourceScope

const KubernetesResourceScopeNamespaced = engine.KubernetesResourceScopeNamespaced

const KubernetesResourceScopeCluster = engine.KubernetesResourceScopeCluster

type KubernetesResourceDescriptor = engine.KubernetesResourceDescriptor

type KubernetesResourceCatalog = engine.KubernetesResourceCatalog

// DefaultKubernetesResourceCatalogは現在の組み込みCatalog revisionを返します。
func DefaultKubernetesResourceCatalog() KubernetesResourceCatalog {
	return engine.DefaultKubernetesResourceCatalog()
}

// NewKubernetesResourceCatalogは現在の組み込みCatalogに追加Descriptorを設定して返します。
func NewKubernetesResourceCatalog(additionalResources ...KubernetesResourceDescriptor) KubernetesResourceCatalog {
	return engine.NewKubernetesResourceCatalog(additionalResources...)
}

func EvaluateFile(path string) (Bundle, error) {
	return engine.EvaluateFile(path)
}

// EvaluateFileContextは呼び出し元のContextを使ってMCLファイルを評価します。
func EvaluateFileContext(ctx context.Context, path string) (Bundle, error) {
	return engine.EvaluateFileContext(ctx, path)
}

// EvaluateFileForOperation は指定した操作向けにMCLファイルを評価します。
func EvaluateFileForOperation(path, operation string) (Bundle, error) {
	return engine.EvaluateFileForOperation(path, operation)
}

// EvaluateFileForOperationContextは呼び出し元のContextを使って指定した操作向けにMCLファイルを評価します。
func EvaluateFileForOperationContext(ctx context.Context, path, operation string) (Bundle, error) {
	return engine.EvaluateFileForOperationContext(ctx, path, operation)
}

func UpdateFile(path string) error { return engine.UpdateFile(path) }

// UpdateFileContextは呼び出し元のContextを使って依存ロックを更新します。
func UpdateFileContext(ctx context.Context, path string) error {
	return engine.UpdateFileContext(ctx, path)
}

func EvaluateSource(path, source string) (Bundle, error) {
	return engine.EvaluateSource(path, source)
}

// EvaluateSourceContextは呼び出し元のContextを使ってソーステキストを評価します。
func EvaluateSourceContext(ctx context.Context, path, source string) (Bundle, error) {
	return engine.EvaluateSourceContext(ctx, path, source)
}

// EvaluateSourceWithOverlays は保存前のモジュール内容を使ってソースを評価します。
func EvaluateSourceWithOverlays(path, source string, overlays map[string]string) (Bundle, error) {
	return engine.EvaluateSourceWithOverlays(path, source, overlays)
}

// EvaluateSourceWithOverlaysContextは呼び出し元のContextを使ってソースとオーバーレイを評価します。
func EvaluateSourceWithOverlaysContext(ctx context.Context, path, source string, overlays map[string]string) (Bundle, error) {
	return engine.EvaluateSourceWithOverlaysContext(ctx, path, source, overlays)
}

// EvaluateSourceWithOverlaysAndDependencies は評価結果と読み込んだモジュールのパスを返します。
func EvaluateSourceWithOverlaysAndDependencies(path, source string, overlays map[string]string) (Bundle, []string, error) {
	return engine.EvaluateSourceWithOverlaysAndDependencies(path, source, overlays)
}

// EvaluateSourceWithOverlaysAndDependenciesContextは呼び出し元のContextを使ってソースを評価し、読み込んだモジュールのパスを返します。
func EvaluateSourceWithOverlaysAndDependenciesContext(ctx context.Context, path, source string, overlays map[string]string) (Bundle, []string, error) {
	return engine.EvaluateSourceWithOverlaysAndDependenciesContext(ctx, path, source, overlays)
}

func Render(bundle Bundle) ([]byte, error) { return engine.Render(bundle) }

// RenderWithSourceMapはmain phaseのYAMLとManifest source mapを返します。
func RenderWithSourceMap(bundle Bundle) ([]byte, ManifestSourceMap, error) {
	return engine.RenderWithSourceMap(bundle)
}

func ValidateBundleOutput(bundle Bundle) error {
	return engine.ValidateBundleOutput(bundle)
}

// ValidateBundleOutputWithCatalogは指定CatalogとBundle内CRDを使って出力検証します。
func ValidateBundleOutputWithCatalog(bundle Bundle, catalog KubernetesResourceCatalog) error {
	return engine.ValidateBundleOutputWithCatalog(bundle, catalog)
}

func RenderPhase(bundle Bundle, phase string) ([]byte, error) {
	return engine.RenderPhase(bundle, phase)
}

// RenderPhaseWithSourceMapは指定phaseのYAMLとManifest source mapを返します。
func RenderPhaseWithSourceMap(bundle Bundle, phase string) ([]byte, ManifestSourceMap, error) {
	return engine.RenderPhaseWithSourceMap(bundle, phase)
}

type RenderedFile = engine.RenderedFile

func RenderOperation(bundle Bundle, operation string) ([]RenderedFile, error) {
	return engine.RenderOperation(bundle, operation)
}

// LoadKubernetesResourceCatalogForFileは組み込みResource Catalogを返します。
//
// Deprecated: プロジェクト設定からResource Descriptorを読み込む機能はありません。DefaultKubernetesResourceCatalogを使ってください。
func LoadKubernetesResourceCatalogForFile(entryPath string) (KubernetesResourceCatalog, error) {
	return engine.LoadKubernetesResourceCatalogForFile(entryPath)
}

type SourceDiagnosticLocation = engine.SourceDiagnosticLocation

type SourceOriginKind = engine.SourceOriginKind

const SourceOriginMCL = engine.SourceOriginMCL

const SourceOriginMCLOverlayBase = engine.SourceOriginMCLOverlayBase

const SourceOriginMCLOverlayPatch = engine.SourceOriginMCLOverlayPatch

const SourceOriginKustomizeInput = engine.SourceOriginKustomizeInput

const SourceOriginKustomizeChange = engine.SourceOriginKustomizeChange

const SourceOriginHelmExpression = engine.SourceOriginHelmExpression

const SourceOriginHelmBranch = engine.SourceOriginHelmBranch

const SourceOriginHelmFallback = engine.SourceOriginHelmFallback

type SourceOriginMultiplicity = engine.SourceOriginMultiplicity

const SourceOriginUnique = engine.SourceOriginUnique

const SourceOriginRepeated = engine.SourceOriginRepeated

const SourceOriginAmbiguous = engine.SourceOriginAmbiguous

type SourceDiagnosticError = engine.SourceDiagnosticError

// SourceDiagnosticFromError はエラー列からソース診断を取得します。
func SourceDiagnosticFromError(err error) (SourceDiagnosticError, bool) {
	return engine.SourceDiagnosticFromError(err)
}

type ManifestSourceMap = engine.ManifestSourceMap

type ManifestDocumentSourceMap = engine.ManifestDocumentSourceMap

type ManifestSourceMapEntry = engine.ManifestSourceMapEntry

type ManifestSourceOrigin = engine.ManifestSourceOrigin

type Kind = engine.Kind

const Null = engine.Null

const Bool = engine.Bool

const Integer = engine.Integer

const Decimal = engine.Decimal

const String = engine.String

const List = engine.List

const AdapterBundle = engine.AdapterBundle

const Map = engine.Map

const Replacement = engine.Replacement

const Function = engine.Function

const Lambda = engine.Lambda

const Node = engine.Node

const Rule = engine.Rule

type Value = engine.Value

type RuleFailure = engine.RuleFailure

type NodeIdentity = engine.NodeIdentity

type Entry = engine.Entry

var ToolchainVersion = engine.ToolchainVersion

type BuildIdentity = engine.BuildIdentity

func CurrentBuildIdentity() BuildIdentity {
	return engine.CurrentBuildIdentity()
}

func init() {
	engine.SetToolchainVersionProvider(func() string { return ToolchainVersion })
}
