// Engineで繰り返し現れる文字列リテラルを定数へ寄せたもの。
package engine

// MCLの組み込み型名。
const (
	typeAny      = "Any"
	typeBool     = "Bool"
	typeDecimal  = "Decimal"
	typeFunction = "Function"
	typeInt      = "Int"
	typeList     = "List"
	typeMap      = "Map"
	typeNode     = "Node"
	typeNull     = "Null"
	typeString   = "String"
)

// JSON Schemaの型名。OpenAPIやKubernetesのスキーマ型をMCLの型へ対応付けるときに使う。
const (
	schemaTypeArray   = "array"
	schemaTypeBoolean = "boolean"
	schemaTypeInteger = "integer"
	schemaTypeNull    = "null"
	schemaTypeNumber  = "number"
	schemaTypeObject  = "object"
	schemaTypeString  = "string"
)

// 標準ライブラリ関数の名前。
const (
	fnClone           = "clone"
	fnConcat          = "concat"
	fnContains        = "contains"
	fnExactlyOne      = "exactlyOne"
	fnFilter          = "filter"
	fnFold            = "fold"
	fnHelmRender      = "helm.render"
	fnKeyOf           = "keyOf"
	fnKustomizeBuild  = "kustomize.build"
	fnLen             = "len"
	fnLifecycleAttach = "lifecycle.attach"
	fnMap             = "map"
	fnManifestLoad    = "manifest.load"
	fnMergeByKey      = "mergeByKey"
	fnReplace         = "replace"
	fnReplaceAt       = "replaceAt"
	fnUnion           = "union"
)

// 標準ライブラリ関数の引数名。
const (
	paramIndex  = "index"
	paramKey    = "key"
	paramSource = "source"
	paramValue  = "value"
	paramValues = "values"
)

// 構文ノードの種別。
const (
	statementEmit     = "emit"
	statementFn       = "fn"
	statementImport   = "import"
	statementLet      = "let"
	statementRule     = "rule"
	statementType     = "type"
	statementValidate = "validate"
)

// 論理演算子と単項演算子。
const (
	operatorAnd = "and"
	operatorNot = "not"
	operatorOr  = "or"
)

// ライフサイクルイベントの名前。
const (
	phaseMain           = "main"
	phaseBootstrap      = "bootstrap"
	phaseBeforeCreate   = "before.create"
	phaseAfterCreate    = "after.create"
	phaseBeforeUpdate   = "before.update"
	phaseAfterUpdate    = "after.update"
	phaseBeforeDelete   = "before.delete"
	phaseAfterDelete    = "after.delete"
	phaseBeforeRollback = "before.rollback"
	phaseAfterRollback  = "after.rollback"
	phaseVerify         = "verify"
)

// ライフサイクルで扱う操作名。
const (
	operationCreate   = "create"
	operationUpdate   = "update"
	operationDelete   = "delete"
	operationRollback = "rollback"
	operationVerify   = "verify"
)

// YAMLのタグ名。
const (
	yamlTagNull   = "!!null"
	yamlTagBool   = "!!bool"
	yamlTagInt    = "!!int"
	yamlTagFloat  = "!!float"
	yamlTagString = "!!str"
)

// ライフサイクルのクリーンアップタイミング。
const (
	cleanupBeforeNextRun = "before-next-run"
	cleanupOnFailure     = "on-failure"
	cleanupOnSuccess     = "on-success"
)

// URLスキーム。
const schemeHTTPS = "https"

// ソース診断のコード。
const codeMCLType = "MCL-TYPE"
