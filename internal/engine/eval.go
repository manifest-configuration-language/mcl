package engine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"strings"

	"github.com/shopspring/decimal"
)

type Bundle struct {
	Artifacts []Artifact
}

type Artifact struct {
	Value     Value
	Lifecycle Lifecycle
}

type Lifecycle struct {
	Events   []string
	Priority int
	Cleanup  []string
	Wait     bool
	Origin   string
}

type functionBudget struct {
	//nolint:containedctx // Contextは評価中に再帰する関数呼び出し間で共有します。
	ctx        context.Context
	steps      int
	depth      int
	iterations int
}

const (
	maxEvaluationSteps  = 1_000_000
	maxBundleValueNodes = 300_000
	maxCallDepth        = 256
	maxIterations       = 100_000
)

func (budget *functionBudget) consumeStep() error {
	if budget == nil {
		return nil
	}
	if budget.ctx != nil {
		if err := budget.ctx.Err(); err != nil {
			return err
		}
	}
	budget.steps++
	if budget.steps > maxEvaluationSteps {
		return fmt.Errorf("evaluation step limit exceeds %d", maxEvaluationSteps)
	}

	return nil
}

func (budget *functionBudget) consumeIteration() error {
	if budget == nil {
		return nil
	}
	if budget.ctx != nil {
		if err := budget.ctx.Err(); err != nil {
			return err
		}
	}
	budget.iterations++
	if budget.iterations > maxIterations {
		return fmt.Errorf("iteration limit exceeds %d", maxIterations)
	}

	return nil
}

func (budget *functionBudget) enterCall() (func(), error) {
	if budget == nil {
		return func() {}, nil
	}
	if budget.ctx != nil {
		if err := budget.ctx.Err(); err != nil {
			return nil, err
		}
	}
	if budget.depth >= maxCallDepth {
		return nil, fmt.Errorf("function call depth exceeds %d", maxCallDepth)
	}
	budget.depth++

	return func() { budget.depth-- }, nil
}

type Diagnostic struct {
	Code     string
	Line     int
	Message  string
	Severity int
	Span     SourceSpan
	Actions  []DiagnosticAction
}

const DiagnosticActionRemoveUnreachableDefinition = "removeUnreachableDefinition"

type DiagnosticAction struct {
	Kind  string
	Edits []DiagnosticTextEdit
}

type DiagnosticTextEdit struct {
	Path    string
	Span    SourceSpan
	NewText string
}

func LintAST(module ModuleAST) []Diagnostic {
	type lintDefinition struct {
		kind     string
		metadata NodeMetadata
		nameSpan SourceSpan
	}
	definitions := make(map[string]lintDefinition)
	rules := make(map[string]*RuleDeclaration)
	for _, node := range module.Nodes {
		switch declaration := node.(type) {
		case *TypeDeclaration:
			definitions[declaration.Name] = lintDefinition{kind: "type", metadata: declaration.NodeMetadata, nameSpan: declaration.NameSpan}
		case *RuleDeclaration:
			definitions[declaration.Name] = lintDefinition{kind: "rule", metadata: declaration.NodeMetadata, nameSpan: declaration.NameSpan}
			rules[declaration.Name] = declaration
		case *LetDeclaration:
			definitions[declaration.Name] = lintDefinition{kind: "value", metadata: declaration.NodeMetadata, nameSpan: declaration.NameSpan}
		case *FunctionDeclaration:
			definitions[declaration.Name] = lintDefinition{kind: "function", metadata: declaration.NodeMetadata, nameSpan: declaration.NameSpan}
		case *ImportDeclaration:
			definitions[declaration.Name] = lintDefinition{kind: "import alias", metadata: declaration.NodeMetadata, nameSpan: declaration.NameSpan}
		}
	}
	dependencies := make(map[string][]string)
	var roots []string
	for _, node := range module.Nodes {
		switch node := node.(type) {
		case *LetDeclaration:
			if node.Expression != nil {
				dependencies[node.Name] = expressionReferences(node.Expression)
			}
			if node.Exported {
				roots = append(roots, node.Name)
			}
			if node.Annotation != nil {
				dependencies[node.Name] = append(dependencies[node.Name], typeReferenceNames(semanticTypeFromSyntax(*node.Annotation))...)
			}
		case *FunctionDeclaration:
			if node.Function != nil {
				parameters := make([]string, len(node.Function.Parameters))
				for index, parameter := range node.Function.Parameters {
					parameters[index] = parameter.Name
				}
				functionBody := &LambdaExpr{Parameters: parameters, Body: node.Function.Expression}
				dependencies[node.Name] = expressionReferences(functionBody)
				for _, parameter := range node.Function.Parameters {
					dependencies[node.Name] = append(dependencies[node.Name], typeReferenceNames(semanticTypeFromSyntax(parameter.Type))...)
				}
				if node.Function.ReturnType != nil {
					dependencies[node.Name] = append(dependencies[node.Name], typeReferenceNames(semanticTypeFromSyntax(*node.Function.ReturnType))...)
				}
			}
			if node.Exported {
				roots = append(roots, node.Name)
			}
		case *TypeDeclaration:
			if node.TypeSpec != nil {
				dependencies[node.Name] = typeReferenceNames(semanticTypeFromSyntax(*node.TypeSpec))
			}
			if node.Exported {
				roots = append(roots, node.Name)
			}
		case *RuleDeclaration:
			if node.Exported {
				roots = append(roots, node.Name)
			}
		case *EmitStatement:
			if node.Expression != nil {
				roots = append(roots, expressionReferences(node.Expression)...)
			}
		case *ValidateStatement:
			if node.Expression != nil {
				roots = append(roots, expressionReferences(node.Expression)...)
			}
			roots = append(roots, expressionReferences(node.RuleExpr)...)
			if node.QueryType != nil {
				roots = append(roots, typeReferenceNames(semanticTypeFromSyntax(*node.QueryType))...)
			}
			if node.QuerySchema != nil {
				roots = append(roots, typeReferenceNames(semanticTypeFromSyntax(*node.QuerySchema))...)
			}
			if node.QueryWhere != nil {
				roots = append(roots, expressionReferences(node.QueryWhere)...)
			}
			if _, exists := rules[node.RuleName]; exists {
				roots = append(roots, node.RuleName)
			}
		}
	}
	for name := range rules {
		dependencies[name] = append(dependencies[name], ruleDependenciesAST(name, rules, make(map[string]bool))...)
	}
	reachable := make(map[string]bool)
	var visit func(string)
	visit = func(name string) {
		if reachable[name] {
			return
		}
		reachable[name] = true
		for _, dependency := range dependencies[name] {
			visit(dependency)
		}
	}
	for _, root := range roots {
		visit(root)
	}
	var diagnostics []Diagnostic
	for name, definition := range definitions {
		if !reachable[name] {
			diagnostics = append(diagnostics, Diagnostic{
				Code: "MCLW001", Line: definition.metadata.Line,
				Message:  fmt.Sprintf("%s %q is not reachable from an emitted value, validation, or exported declaration", definition.kind, name),
				Severity: 2, Span: definition.nameSpan,
				Actions: []DiagnosticAction{{
					Kind:  DiagnosticActionRemoveUnreachableDefinition,
					Edits: []DiagnosticTextEdit{{Path: definition.metadata.SourcePath, Span: definition.metadata.Span}},
				}},
			})
		}
	}
	slices.SortFunc(diagnostics, func(left, right Diagnostic) int {
		if order := cmp.Compare(left.Span.Start, right.Span.Start); order != 0 {
			return order
		}

		return cmp.Compare(left.Line, right.Line)
	})

	return diagnostics
}

func ruleDependenciesAST(name string, rules map[string]*RuleDeclaration, seen map[string]bool) []string {
	if seen[name] {
		return nil
	}
	seen[name] = true
	ruleDeclaration, exists := rules[name]
	if !exists || ruleDeclaration.Rule == nil {
		return nil
	}
	rule := *ruleDeclaration.Rule
	parameters := make(map[string]bool, len(rule.Parameters))
	var dependencies []string
	for _, parameter := range rule.Parameters {
		parameters[parameter.Name] = true
		dependencies = append(dependencies, typeReferenceNames(semanticTypeFromSyntax(parameter.Type))...)
	}
	for _, requirement := range rule.Requirements {
		for _, reference := range expressionReferences(requirement.Expression) {
			if parameters[reference] {
				continue
			}
			if _, isRule := rules[reference]; isRule {
				dependencies = append(dependencies, reference)
				dependencies = append(dependencies, ruleDependenciesAST(reference, rules, seen)...)
			} else {
				dependencies = append(dependencies, reference)
			}
		}
	}

	return dependencies
}

func typeReferenceNames(typeExpr ResolvedType) []string {
	var result []string
	if typeExpr.Name != "" {
		result = append(result, referenceRoot(typeExpr.Name))
	}
	for _, option := range typeExpr.Options {
		result = append(result, typeReferenceNames(option)...)
	}
	if typeExpr.Element != nil {
		result = append(result, typeReferenceNames(*typeExpr.Element)...)
	}
	if typeExpr.AdditionalProperties != nil {
		result = append(result, typeReferenceNames(*typeExpr.AdditionalProperties)...)
	}
	for _, parameter := range typeExpr.Parameters {
		result = append(result, typeReferenceNames(parameter.Type)...)
	}
	if typeExpr.ReturnType != nil {
		result = append(result, typeReferenceNames(*typeExpr.ReturnType)...)
	}
	for _, field := range typeExpr.Fields {
		result = append(result, typeReferenceNames(field.Type)...)
	}

	return result
}

func EvaluateAST(module ModuleAST) (Bundle, error) {
	return evaluateAST(module)
}

// EvaluateASTContextはキャンセル可能なContextと標準ライブラリimportのみで単一モジュールを評価します。
func EvaluateASTContext(ctx context.Context, module ModuleAST) (Bundle, error) {
	if ctx == nil {
		return Bundle{}, errors.New("evaluation context is required")
	}
	if err := ctx.Err(); err != nil {
		return Bundle{}, err
	}
	types, err := typeDefinitionsAST(module)
	if err != nil {
		return Bundle{}, err
	}
	moduleContext := &ModuleContext{Context: ctx, Analysis: &TypeAnalysisContext{}}
	state := &EvaluationState{
		Environment: make(map[string]RuntimeValue), StaticTypes: make(map[string]TypeAnalysis), Types: types,
		Stack: make(map[string]bool), Budget: &functionBudget{ctx: ctx}, Allocations: &runtimeAllocationBudget{}, Module: moduleContext,
	}
	for _, node := range module.Nodes {
		declaration, ok := node.(*ImportDeclaration)
		if !ok {
			continue
		}
		if !strings.HasPrefix(declaration.ModulePath, "std:") {
			return Bundle{}, fmt.Errorf("line %d: only standard-library imports are supported", declaration.Line)
		}
		entries, exists := standardRuntimeLibrary(declaration.ModulePath)
		if !exists {
			return Bundle{}, fmt.Errorf("line %d: standard library module %q is not available", declaration.Line, declaration.ModulePath)
		}
		for _, entry := range entries {
			if builtin, ok := builtinByName(entry.Value.TextValue()); ok && builtin.evaluation == builtinEvaluationAdapter {
				return Bundle{}, fmt.Errorf("line %d: adapter libraries are not supported in single-module evaluation", declaration.Line)
			}
		}
		if _, exists := state.Environment[declaration.Name]; exists {
			return Bundle{}, fmt.Errorf("line %d: %q is already defined", declaration.Line, declaration.Name)
		}
		if err := state.Allocations.consume(len(entries) + 1); err != nil {
			return Bundle{}, sourceDiagnostic("", declaration.Span, err)
		}
		imported := RuntimeValue{Value: runtimeValueData{Kind: Map}, Map: entries}
		state.Environment[declaration.Name] = imported
		if inferred, known := inferRuntimeValueStaticType(imported); known {
			state.StaticTypes[declaration.Name] = inferred
		}
	}
	_, runtimeBundle, err := evaluateModuleASTRuntime(module, state, true) //nolint:contextcheck // EvaluationStateのBudget経由でContextを評価全体へ伝播します。
	if err != nil {
		return Bundle{}, err
	}
	if err := resolveRuntimeBundle(runtimeBundle, state); err != nil { //nolint:contextcheck // EvaluationStateのBudget経由でContextを評価全体へ伝播します。
		return Bundle{}, err
	}
	if err := ctx.Err(); err != nil {
		return Bundle{}, err
	}

	return runtimeBundleToBundle(*runtimeBundle)
}

func evaluateASTRuntime(module ModuleAST) (*RuntimeBundle, *EvaluationState, error) {
	state := &EvaluationState{
		Environment: make(map[string]RuntimeValue), StaticTypes: make(map[string]TypeAnalysis),
		Stack: make(map[string]bool), Budget: &functionBudget{},
	}
	_, bundle, err := evaluateModuleASTRuntime(module, state, true)
	if err != nil {
		return nil, nil, err
	}

	return bundle, state, nil
}

func evaluateModuleASTRuntime(module ModuleAST, state *EvaluationState, entry bool) ([]RuntimeEntry, *RuntimeBundle, error) {
	if state == nil {
		return nil, nil, errors.New("evaluation state is nil")
	}
	if state.Environment == nil {
		state.Environment = make(map[string]RuntimeValue)
	}
	if state.StaticTypes == nil {
		state.StaticTypes = make(map[string]TypeAnalysis)
	}
	if state.Stack == nil {
		state.Stack = make(map[string]bool)
	}
	if state.Budget == nil {
		state.Budget = &functionBudget{}
	}
	if state.Types == nil {
		types, err := typeDefinitionsAST(module)
		if err != nil {
			return nil, nil, err
		}
		state.Types = types
	}
	if err := validateDefinitionCyclesAST(module); err != nil {
		return nil, nil, err
	}

	bundle := &RuntimeBundle{}
	var exports []RuntimeEntry
	for _, node := range module.Nodes {
		if isNilASTNode(node) {
			return nil, nil, fmt.Errorf("line %d: nil AST node", astNodeLine(node))
		}
		line := astNodeLine(node)
		if err := state.Budget.consumeStep(); err != nil {
			return nil, nil, fmt.Errorf("line %d: %w", line, sourceDiagnostic(astNodeSourcePath(node), astNodeSpan(node), err))
		}
		switch declaration := node.(type) {
		case *TypeDeclaration, *ImportDeclaration:
			continue
		case *LetDeclaration:
			if _, exists := state.Environment[declaration.Name]; exists {
				return nil, nil, fmt.Errorf("%q is already defined", declaration.Name)
			}
			var expression Expr = &NullExpr{}
			if declaration.Expression != nil {
				expression = declaration.Expression
			}
			analysis, known, err := inferStaticExprAnalysis(expression, state.StaticTypes, state.Types)
			if err != nil {
				return nil, nil, sourceDiagnostic(declaration.SourcePath, expression.ExprSpan(), err)
			}
			typed, supported, err := typedExpressionFromKnownAnalysis(expression, state, analysis, known)
			if err != nil {
				return nil, nil, err
			}
			if !supported {
				return nil, nil, sourceDiagnostic(declaration.SourcePath, expression.ExprSpan(), fmt.Errorf("expression type %T is not supported by the typed evaluator", expression))
			}
			value, err := evaluateRuntimeExpression(typed, state)
			if err != nil {
				return nil, nil, err
			}
			addRuntimeDependencySourceLocation(&value, declaration.SourcePath, expression.ExprSpan())
			if declaration.Annotation != nil {
				annotation, err := resolveTypeFromSyntax(*declaration.Annotation, state)
				if err != nil {
					return nil, nil, sourceDiagnostic(declaration.SourcePath, declaration.Annotation.Span, err)
				}
				if err := checkRuntimeType(value, annotation, state.Types, make(map[string]bool), state.Module.Analysis); err != nil {
					return nil, nil, sourceDiagnostic(declaration.SourcePath, expression.ExprSpan(), fmt.Errorf("value %q does not match its type: %w", declaration.Name, err))
				}
				value.EnsureTyping().TypeProvenance = append(value.EnsureTyping().TypeProvenance, TypeProvenance{Type: annotation, Types: maps.Clone(state.Types)})
				state.StaticTypes[declaration.Name] = preserveBundleProvenance(analysis, annotation, state.Types)
			} else if known {
				state.StaticTypes[declaration.Name] = analysis
			}
			state.Environment[declaration.Name] = value
			if declaration.Exported {
				exports = append(exports, RuntimeEntry{Key: declaration.Name, Value: value})
			}
		case *FunctionDeclaration:
			if declaration.Function == nil {
				return nil, nil, fmt.Errorf("line %d: missing function definition", line)
			}
			signature, err := functionDefinitionSignature(declaration.Name, declaration.Function, state.StaticTypes, state.Types, state)
			if err != nil {
				return nil, nil, sourceDiagnostic(declaration.SourcePath, functionBodySpan(declaration.Function), err)
			}
			if signature.Type.ReturnType == nil {
				return nil, nil, sourceDiagnostic(declaration.SourcePath, functionBodySpan(declaration.Function), errors.New("function return type is unknown"))
			}
			bindings := maps.Clone(state.StaticTypes)
			if bindings == nil {
				bindings = make(map[string]TypeAnalysis)
			}
			bindings[declaration.Name] = signature
			parameterTypes := make([]ResolvedType, len(declaration.Function.Parameters))
			parameterNames := make([]string, len(declaration.Function.Parameters))
			parameterSpans := make([]SourceSpan, len(declaration.Function.Parameters))
			for index, parameter := range declaration.Function.Parameters {
				parameterNames[index] = parameter.Name
				parameterSpans[index] = parameter.Span
				parameterType, err := resolveTypeFromSyntax(parameter.Type, state)
				if err != nil {
					return nil, nil, sourceDiagnostic(declaration.SourcePath, parameter.Span, err)
				}
				parameterTypes[index] = parameterType
				bindings[parameter.Name] = TypeAnalysis{Type: parameterType}
			}
			bodyType := *signature.Type.ReturnType
			bodyType.Span = SourceSpan{}
			body, supported := typeExpression(declaration.Function.Expression, bodyType, typedEvaluationContext{
				runtimeEnv: state.Environment, staticTypes: bindings, types: state.Types, stack: state.Stack,
				budget: state.Budget, sourcePath: declaration.SourcePath, module: state.Module,
			})
			if !supported {
				return nil, nil, sourceDiagnostic(declaration.SourcePath, functionBodySpan(declaration.Function), fmt.Errorf("expression type %T is not supported by the typed evaluator", declaration.Function.Expression))
			}
			lambdaTyping := &LambdaTyping{ParameterTypes: parameterTypes, ReturnType: signature.Type.ReturnType, TypeScope: maps.Clone(bindings)}
			lambda := RuntimeValue{
				Value:  runtimeValueData{Kind: Lambda, Span: functionBodySpan(declaration.Function), SourcePath: declaration.SourcePath},
				Lambda: &typedLambda{Parameters: parameterNames, ParameterSpans: parameterSpans, Body: body, Scope: maps.Clone(state.Environment), ScopeSpan: functionBodySpan(declaration.Function), Typing: lambdaTyping, ModuleContext: state.Module},
			}
			lambda.Lambda.Scope[declaration.Name] = lambda
			state.Environment[declaration.Name] = lambda
			state.StaticTypes[declaration.Name] = signature
			if declaration.Exported {
				exports = append(exports, RuntimeEntry{Key: declaration.Name, Value: lambda})
			}
		case *RuleDeclaration:
			if declaration.Rule == nil {
				return nil, nil, fmt.Errorf("line %d: missing Rule definition", line)
			}
			parameters := make([]RuleParameter, len(declaration.Rule.Parameters))
			for index, parameter := range declaration.Rule.Parameters {
				parameterType, err := resolveTypeFromSyntax(parameter.Type, state)
				if err != nil {
					return nil, nil, sourceDiagnostic(declaration.SourcePath, parameter.Span, err)
				}
				parameters[index] = RuleParameter{Name: parameter.Name, Type: parameterType, Span: parameter.Span}
			}
			rule := RuntimeRule{
				Name: declaration.Name, Identity: new(runtimeRuleIdentity), ParameterDefinitions: parameters,
				Closure: maps.Clone(state.Environment), StaticTypes: maps.Clone(state.StaticTypes), Types: maps.Clone(state.Types), ModuleContext: state.Module,
			}
			value := RuntimeValue{Value: runtimeValueData{Kind: Rule, Span: declaration.Span, SourcePath: declaration.SourcePath}, Rule: &rule}
			state.Environment[declaration.Name] = value
			ruleParameters := make([]FunctionTypeParameter, len(declaration.Rule.Parameters))
			for index, parameter := range declaration.Rule.Parameters {
				ruleParameters[index] = FunctionTypeParameter{Name: parameter.Name, Type: parameters[index].Type}
			}
			ruleResult := ResolvedType{Name: typeBool}
			state.StaticTypes[declaration.Name] = TypeAnalysis{Type: ResolvedType{Name: typeFunction, Parameters: ruleParameters, ReturnType: &ruleResult}}
			ruleStaticTypes := maps.Clone(rule.StaticTypes)
			for _, parameter := range rule.ParameterDefinitions {
				ruleStaticTypes[parameter.Name] = TypeAnalysis{Type: parameter.Type}
			}
			ruleState := *state
			ruleState.Environment = rule.Closure
			ruleState.StaticTypes = ruleStaticTypes
			for _, requirement := range declaration.Rule.Requirements {
				ruleState.SourcePath = requirement.SourcePath
				expression, supported, err := typedExpressionFromExpr(requirement.Expression, &ruleState)
				if err != nil {
					return nil, nil, sourceDiagnostic(requirement.SourcePath, requirement.Span, fmt.Errorf("line %d: rule %q require %q: %w", requirement.Line, declaration.Name, requirement.Source, err))
				}
				if !supported {
					return nil, nil, sourceDiagnostic(requirement.SourcePath, requirement.Span, fmt.Errorf("line %d: rule %q require %q uses an unsupported expression", requirement.Line, declaration.Name, requirement.Source))
				}
				rule.Requirements = append(rule.Requirements, typedRuleRequirement{
					Expression: expression, Source: requirement.Source, Message: requirement.Message, SourcePath: requirement.SourcePath,
					Line: requirement.Line, Span: requirement.Span,
				})
			}
			if declaration.Exported {
				exports = append(exports, RuntimeEntry{Key: declaration.Name, Value: value})
			}
		case *EmitStatement:
			if !entry {
				return nil, nil, errors.New("imported modules cannot emit values")
			}
			if declaration.Expression == nil {
				return nil, nil, sourceDiagnostic(declaration.SourcePath, declaration.Span, errors.New("emit expression is missing"))
			}
			typed, supported, err := typedExpressionFromExpr(declaration.Expression, state)
			if err != nil {
				return nil, nil, err
			}
			if !supported {
				return nil, nil, sourceDiagnostic(declaration.SourcePath, declaration.Expression.ExprSpan(), fmt.Errorf("expression type %T is not supported by the typed evaluator", declaration.Expression))
			}
			value, err := evaluateRuntimeExpression(typed, state)
			if err != nil {
				return nil, nil, err
			}
			if declaration.Each {
				if value.Kind() != List && value.Kind() != AdapterBundle {
					return nil, nil, sourceDiagnostic(declaration.SourcePath, declaration.Expression.ExprSpan(), errors.New("emit each expects a List"))
				}
				for _, item := range value.List {
					if err := appendRuntimeBundleValue(bundle, item); err != nil {
						return nil, nil, sourceDiagnostic(declaration.SourcePath, declaration.Expression.ExprSpan(), err)
					}
				}
			} else if err := appendRuntimeBundleValue(bundle, value); err != nil {
				return nil, nil, sourceDiagnostic(declaration.SourcePath, declaration.Expression.ExprSpan(), err)
			}
		case *ValidateStatement:
			if !entry {
				return nil, nil, sourceDiagnostic(declaration.SourcePath, declaration.Span, errors.New("imported modules cannot validate values"))
			}
			ruleExpression, supported, err := typedExpressionFromExpr(declaration.RuleExpr, state)
			if err != nil {
				return nil, nil, err
			}
			if !supported {
				return nil, nil, sourceDiagnostic(declaration.SourcePath, declaration.RuleExpr.ExprSpan(), errors.New("validation Rule expression is not supported by the typed evaluator"))
			}
			ruleValue, err := evaluateRuntimeExpression(ruleExpression, state)
			if err != nil {
				return nil, nil, err
			}
			if ruleValue.Kind() != Rule || ruleValue.Rule == nil {
				return nil, nil, sourceDiagnostic(declaration.SourcePath, declaration.RuleExpr.ExprSpan(), typeDiagnostic(errors.New("validate expects a Rule")))
			}
			var validationValue RuntimeValue
			if !declaration.Each && !declaration.Output {
				if declaration.Expression == nil {
					return nil, nil, sourceDiagnostic(declaration.SourcePath, declaration.Span, errors.New("validation target expression is missing"))
				}
				targetExpression, supported, err := typedExpressionFromExpr(declaration.Expression, state)
				if err != nil {
					return nil, nil, err
				}
				if !supported {
					return nil, nil, sourceDiagnostic(declaration.SourcePath, declaration.Expression.ExprSpan(), errors.New("validation target expression is not supported by the typed evaluator"))
				}
				validationValue, err = evaluateRuntimeExpression(targetExpression, state)
				if err != nil {
					return nil, nil, err
				}
			}
			queryType, err := resolveOptionalTypeFromSyntax(declaration.QueryType, state)
			if err != nil {
				return nil, nil, sourceDiagnostic(declaration.SourcePath, declaration.Span, err)
			}
			querySchema, err := resolveOptionalTypeFromSyntax(declaration.QuerySchema, state)
			if err != nil {
				return nil, nil, sourceDiagnostic(declaration.SourcePath, declaration.Span, err)
			}
			validation := RuntimeValidation{
				Rule: *ruleValue.Rule, Line: declaration.Line, Each: declaration.Each, Output: declaration.Output,
				Value:      validationValue,
				SourcePath: declaration.SourcePath, Span: declaration.Span,
				QueryType: queryType, QuerySchema: querySchema,
				MinMatches: declaration.QueryMinMatches, MinMatchesSet: declaration.QueryMinMatchesSet, Types: maps.Clone(state.Types),
			}
			if declaration.QueryWhere != nil {
				if err := validateBooleanExprSelector(declaration.QueryWhere, state.StaticTypes, state.Types); err != nil {
					return nil, nil, sourceDiagnostic(declaration.SourcePath, declaration.QueryWhere.ExprSpan(), err)
				}
				queryType, known, err := inferStaticExprType(declaration.QueryWhere, state.StaticTypes, state.Types)
				if err != nil || !known {
					if err == nil {
						err = errors.New("validation selector type is unknown")
					}

					return nil, nil, sourceDiagnostic(declaration.SourcePath, declaration.QueryWhere.ExprSpan(), err)
				}
				queryExpression, supported, err := typedExpressionFromExpr(declaration.QueryWhere, state)
				if err != nil || !supported {
					if err == nil {
						err = errors.New("validation selector is not supported by the typed evaluator")
					}

					return nil, nil, sourceDiagnostic(declaration.SourcePath, declaration.QueryWhere.ExprSpan(), err)
				}
				queryValue, err := evaluateRuntimeExpression(queryExpression, state)
				if err != nil {
					return nil, nil, err
				}
				if queryType.Name == typeFunction {
					validation.QueryWhere = &queryValue
				}
			}
			bundle.PendingValidations = append(bundle.PendingValidations, validation)
		default:
			return nil, nil, fmt.Errorf("line %d: unknown statement %q", line, astNodeKind(node))
		}
	}

	return exports, bundle, nil
}

func appendRuntimeBundleValue(bundle *RuntimeBundle, value RuntimeValue) error {
	values := []RuntimeValue{value}
	if value.Kind() == AdapterBundle {
		values = value.List
	}
	for _, document := range values {
		lifecycle := Lifecycle{Events: []string{phaseMain}}
		if valueLifecycle := value.Metadata().lifecycleMetadata(); valueLifecycle != nil {
			lifecycle = *valueLifecycle
		}
		if documentLifecycle := document.Metadata().lifecycleMetadata(); documentLifecycle != nil {
			if value.Kind() == AdapterBundle && value.Metadata().lifecycleMetadata() != nil {
				return errors.New("lifecycle metadata cannot be attached around a Bundle that already contains lifecycle metadata")
			}
			lifecycle = *documentLifecycle
		}
		bundle.Artifacts = append(bundle.Artifacts, RuntimeArtifact{Value: document, Lifecycle: lifecycle})
	}

	return nil
}

func addRuntimeDependencySourceLocation(value *RuntimeValue, sourcePath string, sourceSpan SourceSpan) {
	if value == nil || sourcePath == "" || sourceSpan.End <= sourceSpan.Start {
		return
	}
	value.Value.SourceLocations = appendSourceLocations(slices.Clone(value.Value.SourceLocations), nil, sourcePath, sourceSpan, "Let expression")
	for index := range value.List {
		addRuntimeDependencySourceLocation(&value.List[index], sourcePath, sourceSpan)
	}
	for index := range value.Map {
		entry := &value.Map[index]
		entry.KeySourceLocations = appendSourceLocations(slices.Clone(entry.KeySourceLocations), nil, sourcePath, sourceSpan, "Let expression")
		addRuntimeDependencySourceLocation(&entry.Value, sourcePath, sourceSpan)
	}
	if value.Base != nil {
		addRuntimeDependencySourceLocation(value.Base, sourcePath, sourceSpan)
	}
}

func astNodeSpan(node ASTNode) SourceSpan {
	switch declaration := node.(type) {
	case *ImportDeclaration:
		return declaration.Span
	case *TypeDeclaration:
		return declaration.Span
	case *RuleDeclaration:
		return declaration.Span
	case *FunctionDeclaration:
		return declaration.Span
	case *LetDeclaration:
		return declaration.Span
	case *EmitStatement:
		return declaration.Span
	case *ValidateStatement:
		return declaration.Span
	default:
		return SourceSpan{}
	}
}

func evaluateAST(module ModuleAST) (Bundle, error) {
	for _, node := range module.Nodes {
		if declaration, ok := node.(*ImportDeclaration); ok {
			return Bundle{}, fmt.Errorf("line %d: imports require file-based evaluation", declaration.Line)
		}
	}
	runtimeBundle, state, err := evaluateASTRuntime(module)
	if err != nil {
		return Bundle{}, err
	}
	if err := resolveRuntimeBundle(runtimeBundle, state); err != nil {
		return Bundle{}, err
	}

	return runtimeBundleToBundle(*runtimeBundle)
}

func astNodeLine(node ASTNode) int {
	switch node := node.(type) {
	case *ImportDeclaration:
		if node == nil {
			return 0
		}

		return node.Line
	case *TypeDeclaration:
		if node == nil {
			return 0
		}

		return node.Line
	case *RuleDeclaration:
		if node == nil {
			return 0
		}

		return node.Line
	case *FunctionDeclaration:
		if node == nil {
			return 0
		}

		return node.Line
	case *LetDeclaration:
		if node == nil {
			return 0
		}

		return node.Line
	case *EmitStatement:
		if node == nil {
			return 0
		}

		return node.Line
	case *ValidateStatement:
		if node == nil {
			return 0
		}

		return node.Line
	default:
		return 0
	}
}

func astNodeSourcePath(node ASTNode) string {
	switch node := node.(type) {
	case *ImportDeclaration:
		if node == nil {
			return ""
		}

		return node.SourcePath
	case *TypeDeclaration:
		if node == nil {
			return ""
		}

		return node.SourcePath
	case *RuleDeclaration:
		if node == nil {
			return ""
		}

		return node.SourcePath
	case *FunctionDeclaration:
		if node == nil {
			return ""
		}

		return node.SourcePath
	case *LetDeclaration:
		if node == nil {
			return ""
		}

		return node.SourcePath
	case *EmitStatement:
		if node == nil {
			return ""
		}

		return node.SourcePath
	case *ValidateStatement:
		if node == nil {
			return ""
		}

		return node.SourcePath
	default:
		return ""
	}
}

func isNilASTNode(node ASTNode) bool {
	switch node := node.(type) {
	case *ImportDeclaration:
		return node == nil
	case *TypeDeclaration:
		return node == nil
	case *RuleDeclaration:
		return node == nil
	case *FunctionDeclaration:
		return node == nil
	case *LetDeclaration:
		return node == nil
	case *EmitStatement:
		return node == nil
	case *ValidateStatement:
		return node == nil
	default:
		return node == nil
	}
}

func astNodeKind(node ASTNode) string {
	switch node := node.(type) {
	case *ImportDeclaration:
		return statementImport
	case *TypeDeclaration:
		return statementType
	case *RuleDeclaration:
		return statementRule
	case *FunctionDeclaration:
		return statementFn
	case *LetDeclaration:
		return statementLet
	case *EmitStatement:
		return statementEmit
	case *ValidateStatement:
		return statementValidate
	default:
		return fmt.Sprintf("%T", node)
	}
}

func validateDefinitionCyclesAST(module ModuleAST) error {
	dependencies := make(map[string][]string)
	lines := make(map[string]int)
	var order []string
	for _, node := range module.Nodes {
		declaration, ok := node.(*LetDeclaration)
		if !ok || declaration == nil {
			continue
		}
		if declaration.Expression == nil {
			dependencies[declaration.Name] = nil
		} else {
			dependencies[declaration.Name] = expressionReferences(declaration.Expression)
		}
		lines[declaration.Name] = declaration.Line
		order = append(order, declaration.Name)
	}
	states := make(map[string]uint8, len(dependencies))
	path := make([]string, 0, len(dependencies))
	var visit func(string) error
	visit = func(name string) error {
		if states[name] == 2 {
			return nil
		}
		if states[name] == 1 {
			start := slices.Index(path, name)
			cycle := append(slices.Clone(path[start:]), name)

			return fmt.Errorf("line %d: cyclic definitions: %s", lines[name], strings.Join(cycle, " -> "))
		}
		states[name] = 1
		path = append(path, name)
		for _, dependency := range dependencies[name] {
			if _, exists := dependencies[dependency]; exists {
				if err := visit(dependency); err != nil {
					return err
				}
			}
		}
		path = path[:len(path)-1]
		states[name] = 2

		return nil
	}
	for _, name := range order {
		if err := visit(name); err != nil {
			return err
		}
	}

	return nil
}

func functionDefinitionSignature(name string, definition *FunctionDefinition, staticTypes map[string]TypeAnalysis, types map[string]ResolvedType, state *EvaluationState) (TypeAnalysis, error) {
	if definition == nil {
		return TypeAnalysis{}, errors.New("missing function definition")
	}
	bindings := maps.Clone(staticTypes)
	var returnType *ResolvedType
	if definition.ReturnType != nil {
		value, err := resolveTypeFromSyntax(*definition.ReturnType, state)
		if err != nil {
			return TypeAnalysis{}, fmt.Errorf("return type: %w", err)
		}
		value.ScopeID = TypeScopeID(state.SourcePath)
		state.Module.Analysis.registerScope(value.ScopeID, types)
		returnType = &value
	}
	signature := TypeAnalysis{Type: ResolvedType{Name: typeFunction, Parameters: make([]FunctionTypeParameter, 0, len(definition.Parameters)), ReturnType: returnType}}
	parameterNames := make([]string, 0, len(definition.Parameters))
	for _, parameter := range definition.Parameters {
		parameterType, err := resolveTypeFromSyntax(parameter.Type, state)
		if err != nil {
			return TypeAnalysis{}, fmt.Errorf("parameter %q: %w", parameter.Name, err)
		}
		parameterType.ScopeID = TypeScopeID(state.SourcePath)
		state.Module.Analysis.registerScope(parameterType.ScopeID, types)
		if err := validateTypeExpr(parameterType, types, make(map[string]bool)); err != nil {
			return TypeAnalysis{}, fmt.Errorf("parameter %q: %w", parameter.Name, err)
		}
		bindings[parameter.Name] = TypeAnalysis{Type: parameterType}
		parameterNames = append(parameterNames, parameter.Name)
		signature.Type.Parameters = append(signature.Type.Parameters, FunctionTypeParameter{Name: parameter.Name, Type: parameterType})
	}
	if returnType != nil {
		if err := validateTypeExpr(*returnType, types, make(map[string]bool)); err != nil {
			return TypeAnalysis{}, fmt.Errorf("return type: %w", err)
		}
		bindings[name] = signature
	}
	inferredAnalysis, known, err := inferStaticExprAnalysis(definition.Expression, bindings, types)
	if err != nil {
		return TypeAnalysis{}, fmt.Errorf("body: %w", err)
	}
	if !known {
		return TypeAnalysis{}, errors.New("body return type cannot be inferred; recursive functions require an explicit return type")
	}
	inferred := inferredAnalysis.Type
	resolvedInferred, inferredKnown := resolveStaticAlias(inferred, types, make(map[string]bool))
	if inferredKnown && resolvedInferred.Name == typeList {
		if index, found := staticBundleReturnParameter(definition.Expression, parameterNames, bindings, types); found {
			parameterType, parameterKnown := resolveStaticAlias(signature.Type.Parameters[index].Type, types, make(map[string]bool))
			if parameterKnown && parameterType.Name == typeList {
				signature.Facts.ReturnBundleParameter = index
				signature.Facts.ReturnBundleParameterKnown = true
			}
		}
	}
	if returnType == nil {
		value := inferred
		value.ScopeID = TypeScopeID(state.SourcePath)
		state.Module.Analysis.registerScope(value.ScopeID, types)
		signature.Type.ReturnType = &value
	} else {
		compatibility := staticTypeCompatibility(inferred, *returnType, types)
		if compatibility.isDefinitelyIncompatible() {
			return TypeAnalysis{}, typeDiagnostic(fmt.Errorf("body returns %s, declared %s", staticTypeName(inferred), staticTypeName(*returnType)))
		}
		if compatibility == typeCompatibilityUnknown {
			return TypeAnalysis{}, typeDiagnostic(fmt.Errorf("body return type cannot be checked against %s", staticTypeName(*returnType)))
		}
		updated := preserveBundleProvenance(inferredAnalysis, *returnType, types)
		signature.Type.ReturnType = &updated.Type
		signature.Facts.Bundle = updated.Facts.Bundle
		signature.Facts.BundleKnown = updated.Facts.BundleKnown
	}

	return signature, nil
}

func staticBundleReturnParameter(expression Expr, parameterNames []string, bindings map[string]TypeAnalysis, types map[string]ResolvedType) (int, bool) {
	switch node := expression.(type) {
	case *ReferenceExpr:
		index := slices.Index(parameterNames, node.Name)

		return index, index >= 0
	case *CallExpr:
		calleeAnalysis, known, err := inferStaticExprAnalysis(node.Callee, bindings, types)
		if err != nil || !known {
			return 0, false
		}
		calleeType := calleeAnalysis.Type
		calleeType, known = resolveStaticAlias(calleeType, types, make(map[string]bool))
		if !known || calleeType.Name != typeFunction || !calleeAnalysis.Facts.ReturnBundleParameterKnown {
			return 0, false
		}
		arguments, ok := staticExprFunctionCallArguments(calleeType.Parameters, node.Arguments, node.ArgumentNames)
		if !ok || calleeAnalysis.Facts.ReturnBundleParameter < 0 || calleeAnalysis.Facts.ReturnBundleParameter >= len(arguments) {
			return 0, false
		}

		return staticBundleReturnParameter(arguments[calleeAnalysis.Facts.ReturnBundleParameter], parameterNames, bindings, types)
	default:
		return 0, false
	}
}

func referenceRoot(text string) string {
	separator := strings.Index(text, ".")
	if optional := strings.Index(text, "?."); optional >= 0 && (separator < 0 || optional < separator) {
		separator = optional
	}
	if separator < 0 {
		return text
	}

	return text[:separator]
}

func equalValues(left, right Value) bool {
	if left.Kind != right.Kind {
		return false
	}
	switch left.Kind {
	case Null:
		return true
	case Bool:
		return left.Bool == right.Bool
	case Integer, String:
		return left.Text == right.Text
	case Decimal:
		leftDecimal, leftErr := parseRuntimeDecimal(left.Text)
		rightDecimal, rightErr := parseRuntimeDecimal(right.Text)

		return leftErr == nil && rightErr == nil && compareRuntimeDecimals(leftDecimal, rightDecimal) == 0
	default:
		return false
	}
}

func compareValues(left, right runtimeValueData) (int, error) {
	if left.Kind != right.Kind {
		return 0, typeDiagnostic(fmt.Errorf("cannot compare %s with %s", kindName(left.Kind), kindName(right.Kind)))
	}
	switch left.Kind {
	case Integer:
		leftInteger, err := parseRuntimeInteger(left.Text)
		if err != nil {
			return 0, err
		}
		rightInteger, err := parseRuntimeInteger(right.Text)
		if err != nil {
			return 0, err
		}

		return leftInteger.Cmp(rightInteger), nil
	case Decimal:
		leftDecimal, err := parseRuntimeDecimal(left.Text)
		if err != nil {
			return 0, err
		}
		rightDecimal, err := parseRuntimeDecimal(right.Text)
		if err != nil {
			return 0, err
		}

		return compareRuntimeDecimals(leftDecimal, rightDecimal), nil
	case String:
		return cmp.Compare(left.Text, right.Text), nil
	default:
		return 0, typeDiagnostic(fmt.Errorf("%s values cannot be ordered", kindName(left.Kind)))
	}
}

func arithmetic(operator string, left, right runtimeValueData) (runtimeValueData, error) {
	if left.Kind != right.Kind || left.Kind != Integer && left.Kind != Decimal {
		return runtimeValueData{}, typeDiagnostic(fmt.Errorf("operator %s expects operands of the same numeric type (Int or Decimal)", operator))
	}
	if left.Kind == Decimal {
		leftDecimal, err := parseRuntimeDecimal(left.Text)
		if err != nil {
			return runtimeValueData{}, err
		}
		rightDecimal, err := parseRuntimeDecimal(right.Text)
		if err != nil {
			return runtimeValueData{}, err
		}
		if err := validateDecimalOperationInputs(leftDecimal, rightDecimal, operator); err != nil {
			return runtimeValueData{}, err
		}
		if (operator == "/" || operator == "%") && rightDecimal.IsZero() {
			return runtimeValueData{}, errors.New("division by zero")
		}
		var result decimal.Decimal
		switch operator {
		case "+":
			result = leftDecimal.Add(rightDecimal)
		case "-":
			result = leftDecimal.Sub(rightDecimal)
		case "*":
			result = leftDecimal.Mul(rightDecimal)
		case "/":
			result = leftDecimal.DivRound(rightDecimal, 16)
		case "%":
			result = leftDecimal.Mod(rightDecimal)
		}
		if err := validateRuntimeDecimal(result); err != nil {
			return runtimeValueData{}, err
		}

		return runtimeValueData{Kind: Decimal, Text: result.String()}, nil
	}
	leftInteger, err := parseRuntimeInteger(left.Text)
	if err != nil {
		return runtimeValueData{}, err
	}
	rightInteger, err := parseRuntimeInteger(right.Text)
	if err != nil {
		return runtimeValueData{}, err
	}
	leftDigits := integerDigitCount(leftInteger)
	rightDigits := integerDigitCount(rightInteger)
	outputDigitsBound := max(leftDigits, rightDigits)
	switch operator {
	case "+", "-":
		outputDigitsBound++
	case "*":
		outputDigitsBound = leftDigits + rightDigits
	}
	if outputDigitsBound > maxMaterializedNumericDigits {
		return runtimeValueData{}, fmt.Errorf("integer %s intermediate exceeds the %d-digit materialization limit", operator, maxMaterializedNumericDigits)
	}
	if operator == "*" && leftInteger.Sign() != 0 && rightInteger.Sign() != 0 && leftDigits+rightDigits-1 > maxNumericCoefficientDigits {
		return runtimeValueData{}, fmt.Errorf("integer multiplication exceeds the %d-digit limit", maxNumericCoefficientDigits)
	}
	result := new(big.Int)
	switch operator {
	case "+":
		result.Add(leftInteger, rightInteger)
	case "-":
		result.Sub(leftInteger, rightInteger)
	case "*":
		result.Mul(leftInteger, rightInteger)
	case "/":
		if rightInteger.Sign() == 0 {
			return runtimeValueData{}, errors.New("division by zero")
		}
		result.Quo(leftInteger, rightInteger)
	case "%":
		if rightInteger.Sign() == 0 {
			return runtimeValueData{}, errors.New("division by zero")
		}
		result.Rem(leftInteger, rightInteger)
	}
	resultText := result.String()
	if len(strings.TrimPrefix(resultText, "-")) > maxNumericCoefficientDigits {
		return runtimeValueData{}, fmt.Errorf("integer result exceeds the %d-digit limit", maxNumericCoefficientDigits)
	}

	return runtimeValueData{Kind: Integer, Text: resultText}, nil
}

func mergedOverrideSourceLocations(base, patch runtimeValueData) []SourceDiagnosticLocation {
	locations := appendSourceLocations(nil, base.SourceLocations, base.SourcePath, base.Span, "Base value")

	return appendSourceLocations(locations, patch.SourceLocations, patch.SourcePath, patch.Span, "Overlay value")
}

func mergedSourceLocations(base, patch runtimeValueData) []SourceDiagnosticLocation {
	locations := make([]SourceDiagnosticLocation, 0, len(base.SourceLocations)+len(patch.SourceLocations)+2)
	appendLocation := func(location SourceDiagnosticLocation) {
		if location.Path == "" || location.Span.End <= location.Span.Start {
			return
		}
		if slices.ContainsFunc(locations, func(existing SourceDiagnosticLocation) bool {
			return existing.Path == location.Path && existing.Span == location.Span
		}) {
			return
		}
		locations = append(locations, location)
	}
	for _, location := range base.SourceLocations {
		appendLocation(location)
	}
	if base.SourcePath != "" && base.Span.End > base.Span.Start {
		appendLocation(SourceDiagnosticLocation{Path: base.SourcePath, Span: base.Span, Message: "Base map"})
	}
	for _, location := range patch.SourceLocations {
		appendLocation(location)
	}
	if patch.SourcePath != "" && patch.Span.End > patch.Span.Start {
		appendLocation(SourceDiagnosticLocation{Path: patch.SourcePath, Span: patch.Span, Message: "Overlay map"})
	}

	return locations
}

type mergeKindMismatch struct {
	message string
}

func (err mergeKindMismatch) Error() string {
	return err.message
}

func kindName(kind Kind) string {
	switch kind {
	case Null:
		return typeNull
	case Bool:
		return typeBool
	case Integer:
		return typeInt
	case Decimal:
		return typeDecimal
	case String:
		return typeString
	case List, AdapterBundle:
		return typeList
	case Map:
		return typeMap
	case Function:
		return typeFunction
	case Rule:
		return "Rule"
	case Lambda:
		return typeFunction
	case Node:
		return typeNode
	default:
		return "unknown value"
	}
}
