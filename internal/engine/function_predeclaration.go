package engine

import (
	"errors"
	"fmt"
	"maps"
)

func predeclareTopLevelFunctions(module ModuleAST, state *EvaluationState) (map[string]*typedLambda, error) {
	placeholders := make(map[string]*typedLambda)
	for _, node := range module.Nodes {
		declaration, ok := node.(*FunctionDeclaration)
		if !ok {
			continue
		}
		if declaration.Function == nil {
			return nil, fmt.Errorf("line %d: missing function definition", declaration.Line)
		}
		if _, exists := state.Environment[declaration.Name]; exists {
			return nil, fmt.Errorf("line %d: %q is already defined", declaration.Line, declaration.Name)
		}
		if _, exists := placeholders[declaration.Name]; exists {
			return nil, fmt.Errorf("line %d: %q is already defined", declaration.Line, declaration.Name)
		}
		if err := state.Allocations.consume(1); err != nil {
			return nil, sourceDiagnostic(declaration.SourcePath, declaration.NameSpan, err)
		}

		signature, err := predeclaredFunctionSignature(declaration, state)
		if err != nil {
			return nil, sourceDiagnostic(declaration.SourcePath, functionBodySpan(declaration.Function), err)
		}
		if signature.Type.ReturnType != nil {
			state.StaticTypes[declaration.Name] = signature
		}
		placeholder := new(typedLambda)
		state.Environment[declaration.Name] = RuntimeValue{
			Value:  runtimeValueData{Kind: Lambda, Span: functionBodySpan(declaration.Function), SourcePath: declaration.SourcePath},
			Lambda: placeholder,
		}
		placeholders[declaration.Name] = placeholder
	}

	return placeholders, nil
}

func predeclaredFunctionSignature(declaration *FunctionDeclaration, state *EvaluationState) (TypeAnalysis, error) {
	definition := declaration.Function
	if definition == nil {
		return TypeAnalysis{}, errors.New("missing function definition")
	}
	if state.Module == nil {
		state.Module = &ModuleContext{Analysis: &TypeAnalysisContext{}}
	}
	if state.Module.Analysis == nil {
		state.Module.Analysis = &TypeAnalysisContext{}
	}

	signature := TypeAnalysis{Type: ResolvedType{Name: typeFunction, Parameters: make([]FunctionTypeParameter, 0, len(definition.Parameters))}}
	if definition.ReturnType != nil {
		returnType, err := resolveTypeFromSyntax(*definition.ReturnType, state)
		if err != nil {
			return TypeAnalysis{}, fmt.Errorf("return type: %w", err)
		}
		returnType.ScopeID = TypeScopeID(declaration.SourcePath)
		state.Module.Analysis.registerScope(returnType.ScopeID, state.Types)
		if err := validateTypeExpr(returnType, state.Types, make(map[string]bool)); err != nil {
			return TypeAnalysis{}, fmt.Errorf("return type: %w", err)
		}
		signature.Type.ReturnType = &returnType
	}
	for _, parameter := range definition.Parameters {
		parameterType, err := resolveTypeFromSyntax(parameter.Type, state)
		if err != nil {
			return TypeAnalysis{}, fmt.Errorf("parameter %q: %w", parameter.Name, err)
		}
		parameterType.ScopeID = TypeScopeID(declaration.SourcePath)
		state.Module.Analysis.registerScope(parameterType.ScopeID, state.Types)
		if err := validateTypeExpr(parameterType, state.Types, make(map[string]bool)); err != nil {
			return TypeAnalysis{}, fmt.Errorf("parameter %q: %w", parameter.Name, err)
		}
		signature.Type.Parameters = append(signature.Type.Parameters, FunctionTypeParameter{
			Name: parameter.Name, Type: parameterType, Optional: parameter.Default != nil,
		})
	}
	signature.typeContext = state.Module.Analysis

	return signature, nil
}

type functionCompilationContext struct {
	declarations    map[string]*FunctionDeclaration
	ruleReferences  map[string][]string
	indices         map[string]int
	letIndices      map[string]int
	runtimeIndices  map[string]int
	placeholders    map[string]*typedLambda
	baseEnvironment map[string]RuntimeValue
	baseStaticTypes map[string]TypeAnalysis
	compiled        map[string]bool
	compiling       map[string]bool
}

func newFunctionCompilationContext(module ModuleAST, placeholders map[string]*typedLambda, state *EvaluationState) *functionCompilationContext {
	context := &functionCompilationContext{
		declarations: make(map[string]*FunctionDeclaration), ruleReferences: make(map[string][]string), indices: make(map[string]int),
		letIndices: make(map[string]int), runtimeIndices: make(map[string]int), placeholders: placeholders,
		baseEnvironment: maps.Clone(state.Environment), baseStaticTypes: maps.Clone(state.StaticTypes),
		compiled: make(map[string]bool), compiling: make(map[string]bool),
	}
	for index, node := range module.Nodes {
		switch declaration := node.(type) {
		case *FunctionDeclaration:
			context.declarations[declaration.Name] = declaration
			context.indices[declaration.Name] = index
		case *LetDeclaration:
			context.runtimeIndices[declaration.Name] = index
			context.letIndices[declaration.Name] = index
		case *RuleDeclaration:
			context.ruleReferences[declaration.Name] = ruleDefinitionReferences(declaration.Rule)
			context.runtimeIndices[declaration.Name] = index
		}
	}

	return context
}

func (context *functionCompilationContext) compileReferencedFunctions(expression Expr, declarationIndex int, state *EvaluationState) error {
	for _, name := range expressionReferences(expression) {
		if _, exists := context.declarations[name]; exists {
			if err := context.prepareFunction(name, declarationIndex, state); err != nil {
				return err
			}
		}
		for _, reference := range context.ruleReferences[name] {
			if _, exists := context.declarations[reference]; !exists {
				continue
			}
			if err := context.prepareFunction(reference, declarationIndex, state); err != nil {
				return err
			}
		}
	}

	return nil
}

func ruleDefinitionReferences(definition *RuleDefinition) []string {
	if definition == nil {
		return nil
	}
	locals := make(map[string]bool, len(definition.Parameters))
	for _, parameter := range definition.Parameters {
		locals[parameter.Name] = true
	}
	var references []string
	for _, requirement := range definition.Requirements {
		for _, reference := range expressionReferences(requirement.Expression) {
			if !locals[reference] {
				references = append(references, reference)
			}
		}
	}

	return references
}

func (context *functionCompilationContext) prepareFunction(name string, declarationIndex int, state *EvaluationState) error {
	declaration := context.declarations[name]
	if declaration == nil {
		return fmt.Errorf("function %q is not declared", name)
	}
	if context.compiling[name] {
		return nil
	}
	context.compiling[name] = true
	defer delete(context.compiling, name)

	if err := context.validateCapturedLets(declaration, declarationIndex, state); err != nil {
		return err
	}
	for _, reference := range functionDefinitionReferences(declaration.Function) {
		if _, exists := context.declarations[reference]; !exists {
			continue
		}
		if err := context.prepareFunction(reference, declarationIndex, state); err != nil {
			return err
		}
	}
	if context.compiled[name] {
		return nil
	}
	if err := state.Budget.consumeStep(); err != nil {
		return sourceDiagnostic(declaration.SourcePath, functionBodySpan(declaration.Function), err)
	}
	functionState := context.lexicalState(name, state)
	value, signature, err := compileFunctionDeclaration(declaration, functionState)
	if err != nil {
		return err
	}
	if placeholder := context.placeholders[name]; placeholder != nil {
		*placeholder = *value.Lambda
		value.Lambda = placeholder
	}
	state.Environment[name] = value
	state.StaticTypes[name] = signature
	context.compiled[name] = true

	return nil
}

func (context *functionCompilationContext) lexicalState(name string, state *EvaluationState) *EvaluationState {
	functionState := *state
	functionState.Environment = maps.Clone(context.baseEnvironment)
	functionState.StaticTypes = maps.Clone(context.baseStaticTypes)
	functionIndex := context.indices[name]
	for functionName := range context.declarations {
		if context.compiled[functionName] {
			functionState.StaticTypes[functionName] = state.StaticTypes[functionName]
		}
	}
	for declarationName, declarationIndex := range context.runtimeIndices {
		if declarationIndex >= functionIndex {
			continue
		}
		if value, initialized := state.Environment[declarationName]; initialized {
			functionState.Environment[declarationName] = value
		}
		if analysis, known := state.StaticTypes[declarationName]; known {
			functionState.StaticTypes[declarationName] = analysis
		}
	}

	return &functionState
}

func (context *functionCompilationContext) validateCapturedLets(declaration *FunctionDeclaration, declarationIndex int, state *EvaluationState) error {
	for _, reference := range functionDefinitionReferences(declaration.Function) {
		letIndex, isLet := context.letIndices[reference]
		if !isLet {
			continue
		}
		if letIndex > context.indices[declaration.Name] {
			return sourceDiagnostic(declaration.SourcePath, functionBodySpan(declaration.Function), fmt.Errorf("function %q cannot reference later let %q", declaration.Name, reference))
		}
		if letIndex >= declarationIndex {
			if _, initialized := state.Environment[reference]; !initialized {
				return sourceDiagnostic(declaration.SourcePath, functionBodySpan(declaration.Function), fmt.Errorf("function %q requires let %q to be initialized before it is called", declaration.Name, reference))
			}
		}
	}

	return nil
}

func functionDefinitionReferences(definition *FunctionDefinition) []string {
	if definition == nil {
		return nil
	}
	var references []string
	appendReferences := func(expression Expr, locals map[string]bool) {
		for _, reference := range expressionReferences(expression) {
			if !locals[reference] {
				references = append(references, reference)
			}
		}
	}
	locals := make(map[string]bool, len(definition.Parameters)+len(definition.Locals))
	for _, parameter := range definition.Parameters {
		appendReferences(parameter.Default, locals)
		locals[parameter.Name] = true
	}
	for _, local := range definition.Locals {
		appendReferences(local.Expression, locals)
		locals[local.Name] = true
	}
	appendReferences(definition.Expression, locals)

	return references
}

func compileFunctionDeclaration(declaration *FunctionDeclaration, state *EvaluationState) (RuntimeValue, TypeAnalysis, error) {
	if declaration.Function == nil {
		return RuntimeValue{}, TypeAnalysis{}, fmt.Errorf("line %d: missing function definition", declaration.Line)
	}
	definition := declaration.Function
	signature, err := functionDefinitionSignature(declaration.Name, definition, state.StaticTypes, state.Types, state)
	if err != nil {
		return RuntimeValue{}, TypeAnalysis{}, sourceDiagnostic(declaration.SourcePath, functionBodySpan(definition), err)
	}
	signature.typeContext = state.Module.Analysis
	if signature.Type.ReturnType == nil {
		return RuntimeValue{}, TypeAnalysis{}, sourceDiagnostic(declaration.SourcePath, functionBodySpan(definition), errors.New("function return type is unknown"))
	}
	bindings := maps.Clone(state.StaticTypes)
	if bindings == nil {
		bindings = make(map[string]TypeAnalysis)
	}
	bindings[declaration.Name] = signature
	parameterTypes := make([]ResolvedType, len(definition.Parameters))
	parameterNames := make([]string, len(definition.Parameters))
	parameterSpans := make([]SourceSpan, len(definition.Parameters))
	defaults := make([]*typedExpression, len(definition.Parameters))
	defaultBindings := maps.Clone(state.StaticTypes)
	if defaultBindings == nil {
		defaultBindings = make(map[string]TypeAnalysis)
	}
	defaultBindings[declaration.Name] = signature
	for index, parameter := range definition.Parameters {
		parameterNames[index] = parameter.Name
		parameterSpans[index] = parameter.Span
		parameterType, err := resolveTypeFromSyntax(parameter.Type, state)
		if err != nil {
			return RuntimeValue{}, TypeAnalysis{}, sourceDiagnostic(declaration.SourcePath, parameter.Span, err)
		}
		parameterTypes[index] = parameterType
		if parameter.Default != nil {
			analysis, known, err := inferStaticExprAnalysisWithExpected(parameter.Default, &parameterType, defaultBindings, state.Types)
			if err != nil {
				return RuntimeValue{}, TypeAnalysis{}, sourceDiagnostic(declaration.SourcePath, parameter.Default.ExprSpan(), err)
			}
			if !known || staticTypeCompatibility(analysis.Type, parameterType, state.Types).isDefinitelyIncompatible() {
				return RuntimeValue{}, TypeAnalysis{}, sourceDiagnostic(declaration.SourcePath, parameter.Default.ExprSpan(), fmt.Errorf("default for parameter %q does not match its declared type", parameter.Name))
			}
			typed, supported, err := typedExpressionFromKnownAnalysisWithStaticTypes(parameter.Default, state, analysis, true, defaultBindings)
			if err != nil {
				return RuntimeValue{}, TypeAnalysis{}, err
			}
			if !supported {
				return RuntimeValue{}, TypeAnalysis{}, sourceDiagnostic(declaration.SourcePath, parameter.Default.ExprSpan(), fmt.Errorf("expression type %T is not supported by the typed evaluator", parameter.Default))
			}
			defaults[index] = &typed
		}
		bindings[parameter.Name] = TypeAnalysis{Type: parameterType}
		defaultBindings[parameter.Name] = TypeAnalysis{Type: parameterType}
	}
	locals := make([]typedLocalBinding, 0, len(definition.Locals))
	for _, local := range definition.Locals {
		var annotation *ResolvedType
		if local.Annotation != nil {
			resolved, err := resolveTypeFromSyntax(*local.Annotation, state)
			if err != nil {
				return RuntimeValue{}, TypeAnalysis{}, sourceDiagnostic(declaration.SourcePath, local.Annotation.Span, err)
			}
			typeScope := TypeScopeID(declaration.SourcePath)
			resolved = inheritStaticTypeScope(resolved, typeScope)
			state.Module.Analysis.registerScope(typeScope, state.Types)
			annotation = &resolved
		}
		analysis, known, err := inferStaticExprAnalysisWithExpected(local.Expression, annotation, bindings, state.Types)
		if err != nil {
			return RuntimeValue{}, TypeAnalysis{}, sourceDiagnostic(declaration.SourcePath, local.Expression.ExprSpan(), err)
		}
		if !known {
			return RuntimeValue{}, TypeAnalysis{}, sourceDiagnostic(declaration.SourcePath, local.Expression.ExprSpan(), fmt.Errorf("cannot infer local variable %q", local.Name))
		}
		typed, supported, err := typedExpressionFromKnownAnalysisWithStaticTypes(local.Expression, state, analysis, true, bindings)
		if err != nil {
			return RuntimeValue{}, TypeAnalysis{}, err
		}
		if !supported {
			return RuntimeValue{}, TypeAnalysis{}, sourceDiagnostic(declaration.SourcePath, local.Expression.ExprSpan(), fmt.Errorf("expression type %T is not supported by the typed evaluator", local.Expression))
		}
		if annotation != nil {
			if staticTypeCompatibility(analysis.Type, *annotation, state.Types).isDefinitelyIncompatible() {
				return RuntimeValue{}, TypeAnalysis{}, sourceDiagnostic(declaration.SourcePath, local.Expression.ExprSpan(), fmt.Errorf("local %q does not match its declared type", local.Name))
			}
			analysis = preserveBundleProvenance(analysis, *annotation, state.Types)
		}
		bindings[local.Name] = analysis
		locals = append(locals, typedLocalBinding{Name: local.Name, Expression: typed, Annotation: annotation, Span: local.Span})
	}
	bodyType := *signature.Type.ReturnType
	bodyType.Span = SourceSpan{}
	bodyContext := typedEvaluationContext{
		runtimeEnv: state.Environment, frame: state.RuntimeFrame, staticTypes: bindings, types: state.Types, stack: state.Stack,
		budget: state.Budget, sourcePath: declaration.SourcePath, module: state.Module,
	}
	if err := builtinKubernetesStaticExpressionDiagnostic(definition.Expression, bodyContext); err != nil {
		return RuntimeValue{}, TypeAnalysis{}, err
	}
	body, supported := typeExpression(definition.Expression, bodyType, bodyContext)
	if !supported {
		return RuntimeValue{}, TypeAnalysis{}, sourceDiagnostic(declaration.SourcePath, functionBodySpan(definition), fmt.Errorf("expression type %T is not supported by the typed evaluator", definition.Expression))
	}
	lambdaTyping := &LambdaTyping{
		ParameterTypes: parameterTypes, ReturnType: signature.Type.ReturnType,
		TypeScope: maps.Clone(bindings), Types: maps.Clone(state.Types),
	}
	lambda := RuntimeValue{
		Value:  runtimeValueData{Kind: Lambda, Span: functionBodySpan(definition), SourcePath: declaration.SourcePath},
		Lambda: &typedLambda{Parameters: parameterNames, ParameterSpans: parameterSpans, Defaults: defaults, Locals: locals, Body: body, Scope: maps.Clone(state.Environment), ScopeSpan: functionBodySpan(definition), Typing: lambdaTyping, ModuleContext: state.Module},
	}
	lambda.Lambda.Scope[declaration.Name] = state.Environment[declaration.Name]

	return lambda, signature, nil
}
