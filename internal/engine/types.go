package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/manifest-configuration-language/mcl/internal/dependency"
	"github.com/santhosh-tekuri/jsonschema/v6"
	jsonschemakind "github.com/santhosh-tekuri/jsonschema/v6/kind"
)

type ResolvedType struct {
	Span                 SourceSpan
	Name                 string
	StringLiteral        string
	StringLiteralKnown   bool
	TypeParameters       []string
	Arguments            []ResolvedType
	SchemaPath           string
	SchemaID             SchemaID
	ScopeID              TypeScopeID
	Element              *ResolvedType
	Options              []ResolvedType
	Fields               []TypeField
	OpenRecord           bool
	AdditionalProperties *ResolvedType
	Optional             bool
	Parameters           []FunctionTypeParameter
	ReturnType           *ResolvedType
}

type FunctionTypeParameter struct {
	Name string
	Type ResolvedType
}

type TypeField struct {
	Name     string
	NameSpan SourceSpan
	Type     ResolvedType
	Optional bool
}

func typeDefinitionsAST(module ModuleAST) (map[string]ResolvedType, error) {
	types := make(map[string]ResolvedType)
	for _, node := range module.Nodes {
		declaration, ok := node.(*TypeDeclaration)
		if !ok || declaration == nil {
			continue
		}
		if isBuiltinType(declaration.Name) {
			return nil, fmt.Errorf("line %d: built-in type %q cannot be redefined", declaration.Line, declaration.Name)
		}
		if _, exists := types[declaration.Name]; exists {
			return nil, fmt.Errorf("line %d: type %q is already defined", declaration.Line, declaration.Name)
		}
		if declaration.TypeSpec == nil {
			return nil, fmt.Errorf("line %d: type %q has no definition", declaration.Line, declaration.Name)
		}
		types[declaration.Name] = semanticTypeFromSyntax(*declaration.TypeSpec)
	}
	for _, node := range module.Nodes {
		declaration, ok := node.(*TypeDeclaration)
		if !ok || declaration == nil {
			continue
		}
		parameters := make(map[string]bool, len(declaration.TypeSpec.TypeParameters))
		for _, parameter := range declaration.TypeSpec.TypeParameters {
			if !identifier(parameter) || parameters[parameter] {
				return nil, fmt.Errorf("line %d: invalid or duplicate type parameter %q", declaration.Line, parameter)
			}
			parameters[parameter] = true
		}
		if err := validateTypeExprWithParameters(types[declaration.Name], types, make(map[string]bool), parameters); err != nil {
			return nil, fmt.Errorf("line %d: %w", declaration.Line, err)
		}
	}
	if err := validateTypeAliasCycles(types); err != nil {
		return nil, err
	}

	return types, nil
}

func isBuiltinType(name string) bool {
	switch name {
	case typeAny, typeNull, typeBool, typeInt, typeDecimal, typeString, typeList, typeMap, typeFunction, typeNode:
		return true
	default:
		return false
	}
}

func validateTypeAliasCycles(types map[string]ResolvedType) error {
	states := make(map[string]uint8, len(types))
	path := make([]string, 0, len(types))
	var visit func(string) error
	visit = func(name string) error {
		if states[name] == 2 {
			return nil
		}
		if states[name] == 1 {
			start := slices.Index(path, name)

			return fmt.Errorf("cyclic type aliases: %s", strings.Join(append(slices.Clone(path[start:]), name), " -> "))
		}
		states[name] = 1
		path = append(path, name)
		for _, reference := range directTypeAliases(types[name]) {
			if _, exists := types[reference]; exists {
				if err := visit(reference); err != nil {
					return err
				}
			}
		}
		path = path[:len(path)-1]
		states[name] = 2

		return nil
	}
	for name := range types {
		if err := visit(name); err != nil {
			return err
		}
	}

	return nil
}

func directTypeAliases(typeExpr ResolvedType) []string {
	parameters := parameterSet(typeExpr.TypeParameters)
	if len(typeExpr.Options) > 0 {
		aliases := make([]string, 0, len(typeExpr.Options))
		for _, option := range typeExpr.Options {
			aliases = append(aliases, directTypeAliases(option)...)
		}

		return aliases
	}
	aliases := make([]string, 0, len(typeExpr.Arguments))
	for _, argument := range typeExpr.Arguments {
		aliases = append(aliases, directTypeAliases(argument)...)
	}
	if typeExpr.AdditionalProperties != nil {
		aliases = append(aliases, directTypeAliases(*typeExpr.AdditionalProperties)...)
	}
	if typeExpr.Name == "" || parameters[typeExpr.Name] {
		return aliases
	}
	if len(typeExpr.Arguments) > 0 && !isBuiltinType(typeExpr.Name) {
		return append(aliases, typeExpr.Name)
	}
	if typeExpr.Element == nil && len(typeExpr.Fields) == 0 && len(typeExpr.Arguments) == 0 {
		return append(aliases, typeExpr.Name)
	}

	return aliases
}

func loadSchemaTypesWithFilesystem(typeExpr *ResolvedType, projectRoot, modulePath string, dependencies *dependency.Lock, files *projectFilesystem, analyses ...*TypeAnalysisContext) error {
	if typeExpr == nil {
		return nil
	}
	var analysis *TypeAnalysisContext
	if len(analyses) != 0 {
		analysis = analyses[0]
	}
	if typeExpr.SchemaPath != "" {
		compiled, err := compileSchema(typeExpr.SchemaPath, projectRoot, modulePath, dependencies, files)
		if err != nil {
			return err
		}
		typeExpr.SchemaID = SchemaID(modulePath + "\x00" + typeExpr.SchemaPath)
		analysis.registerSchema(typeExpr.SchemaID, compiled)
	}
	for index := range typeExpr.Options {
		if err := loadSchemaTypesWithFilesystem(&typeExpr.Options[index], projectRoot, modulePath, dependencies, files, analysis); err != nil {
			return err
		}
	}
	if typeExpr.Element != nil {
		if err := loadSchemaTypesWithFilesystem(typeExpr.Element, projectRoot, modulePath, dependencies, files, analysis); err != nil {
			return err
		}
	}
	for index := range typeExpr.Fields {
		if err := loadSchemaTypesWithFilesystem(&typeExpr.Fields[index].Type, projectRoot, modulePath, dependencies, files, analysis); err != nil {
			return err
		}
	}
	if err := loadSchemaTypesWithFilesystem(typeExpr.AdditionalProperties, projectRoot, modulePath, dependencies, files, analysis); err != nil {
		return err
	}

	return nil
}

func checkRuntimeType(value RuntimeValue, typeExpr ResolvedType, types map[string]ResolvedType, resolving map[string]bool, analyses ...*TypeAnalysisContext) error {
	if err := checkRuntimeTypeValue(value, typeExpr, types, resolving, analyses...); err != nil {
		metadata := value.Metadata()
		if diagnostic, ok := SourceDiagnosticFromError(err); ok && diagnostic.Path == "" && diagnostic.Span.End <= diagnostic.Span.Start && len(diagnostic.Related) != 0 {
			return err
		}

		return sourceDiagnostic(metadata.SourcePath, metadata.Span, typeDiagnostic(err))
	}

	return nil
}

func checkRuntimeTypeValue(value RuntimeValue, typeExpr ResolvedType, types map[string]ResolvedType, resolving map[string]bool, analyses ...*TypeAnalysisContext) error {
	original := value
	value = runtimeNodeContents(value)
	var analysis *TypeAnalysisContext
	if len(analyses) != 0 {
		analysis = analyses[0]
	}
	if scoped := analysis.scope(typeExpr.ScopeID); scoped != nil {
		types = scoped
	}
	schema := analysis.schema(typeExpr.SchemaID)
	if typeExpr.SchemaPath != "" && schema == nil {
		return fmt.Errorf("schema %q requires file-based evaluation", typeExpr.SchemaPath)
	}
	if schema != nil {
		instance, err := runtimeValueAsJSON(value)
		if err != nil {
			return err
		}
		if err := schema.Validate(instance); err != nil {
			return schemaValidationRuntimeDiagnostic(value, err)
		}

		return nil
	}
	if len(typeExpr.Options) > 0 {
		failures := make([]string, 0, len(typeExpr.Options))
		for _, option := range typeExpr.Options {
			if err := checkRuntimeType(original, option, types, resolving, analysis); err == nil {
				return nil
			} else {
				failures = append(failures, err.Error())
			}
		}

		return fmt.Errorf("value does not match any union member: %s", strings.Join(failures, "; "))
	}
	if typeExpr.Name != "" && (typeExpr.Name != typeMap || typeExpr.Fields == nil) {
		switch typeExpr.Name {
		case typeAny:
			return nil
		case typeNever:
			return errors.New("no value inhabits Never")
		case typeNull:
			return requireRuntimeKind(value, Null, typeExpr.Name)
		case typeBool:
			return requireRuntimeKind(value, Bool, typeExpr.Name)
		case typeInt:
			return requireRuntimeKind(value, Integer, typeExpr.Name)
		case typeDecimal:
			return requireRuntimeKind(value, Decimal, typeExpr.Name)
		case typeString:
			if err := requireRuntimeKind(value, String, typeExpr.Name); err != nil {
				return err
			}
			if typeExpr.StringLiteralKnown && value.TextValue() != typeExpr.StringLiteral {
				return fmt.Errorf("expected string %q, got %q", typeExpr.StringLiteral, value.TextValue())
			}

			return nil
		case typeNode:
			if original.Kind() != Node || original.Base == nil {
				return fmt.Errorf("expected Node, got %s", kindName(original.Kind()))
			}
			if len(typeExpr.Arguments) != 1 {
				return errors.New("node requires a value type")
			}

			return checkRuntimeType(*original.Base, typeExpr.Arguments[0], types, resolving, analysis)
		case typeList:
			if value.Kind() != List && value.Kind() != AdapterBundle {
				return fmt.Errorf("expected List, got %s", kindName(value.Kind()))
			}
			if typeExpr.Element == nil {
				return errors.New("list requires an element type")
			}
			for index, item := range value.List {
				if err := checkRuntimeType(item, *typeExpr.Element, types, resolving, analysis); err != nil {
					return fmt.Errorf("item %d: %w", index, err)
				}
			}

			return nil
		case typeMap:
			if typeExpr.Fields != nil || typeExpr.OpenRecord {
				break
			}
			if value.Kind() != Map {
				return fmt.Errorf("expected Map, got %s", kindName(value.Kind()))
			}
			if typeExpr.Element == nil {
				return errors.New("map requires a value type")
			}
			for _, entry := range value.Map {
				if err := checkRuntimeType(entry.Value, *typeExpr.Element, types, resolving, analysis); err != nil {
					return fmt.Errorf("field %q: %w", entry.Key, err)
				}
			}

			return nil
		case typeFunction:
			if value.Kind() != Lambda || value.Lambda == nil {
				return fmt.Errorf("expected Function, got %s", kindName(value.Kind()))
			}
			if len(typeExpr.Parameters) != len(value.Lambda.Parameters) || typeExpr.ReturnType == nil {
				return errors.New("function signature does not match")
			}
			if value.Lambda.Typing == nil || value.Lambda.Typing.ReturnType == nil || len(value.Lambda.Typing.ParameterTypes) != len(value.Lambda.Parameters) {
				return errors.New("function signature cannot be checked")
			}
			for index, parameter := range typeExpr.Parameters {
				matches, known := staticTypeAssignable(parameter.Type, value.Lambda.Typing.ParameterTypes[index], types)
				if !known || !matches {
					return fmt.Errorf("function parameter %d does not match", index+1)
				}
			}
			matches, known := staticTypeAssignable(*value.Lambda.Typing.ReturnType, *typeExpr.ReturnType, types)
			if !known || !matches {
				return errors.New("function return type does not match")
			}

			return nil
		default:
			definition, exists := types[typeExpr.Name]
			if !exists {
				return fmt.Errorf("unknown type %q", typeExpr.Name)
			}
			if resolving[typeExpr.Name] && definition.Name != "" && definition.Element == nil && definition.AdditionalProperties == nil && !definition.OpenRecord && len(definition.Options) == 0 && len(definition.Fields) == 0 {
				return fmt.Errorf("cyclic type alias involving %q", typeExpr.Name)
			}
			resolving[typeExpr.Name] = true
			instantiated, err := instantiateTypeAlias(typeExpr, definition)
			if err != nil {
				delete(resolving, typeExpr.Name)

				return err
			}
			err = checkRuntimeType(original, instantiated, types, resolving, analysis)
			delete(resolving, typeExpr.Name)
			if err != nil {
				return addTypeDiagnosticContext(err, typeExpr.Name, instantiated.SchemaPath)
			}

			return nil
		}
	}
	if typeExpr.Fields != nil || typeExpr.OpenRecord {
		if value.Kind() != Map {
			return fmt.Errorf("expected Map, got %s", kindName(value.Kind()))
		}
		known := make(map[string]TypeField, len(typeExpr.Fields))
		for _, field := range typeExpr.Fields {
			known[field.Name] = field
			fieldValue, exists := value.MapLookup(field.Name)
			if !exists {
				if field.Optional {
					continue
				}

				return fmt.Errorf("required field %q is missing", field.Name)
			}
			if err := checkRuntimeType(fieldValue, field.Type, types, resolving, analysis); err != nil {
				return fmt.Errorf("field %q: %w", field.Name, err)
			}
		}
		for _, entry := range value.Map {
			if _, exists := known[entry.Key]; exists {
				continue
			}
			if !typeExpr.OpenRecord {
				return runtimeMapKeyDiagnostic(entry, unknownTypeFieldError(entry.Key, typeExpr.Fields))
			}
			if typeExpr.AdditionalProperties != nil {
				if err := checkRuntimeType(entry.Value, *typeExpr.AdditionalProperties, types, resolving, analysis); err != nil {
					return runtimeMapKeyDiagnostic(entry, fmt.Errorf("additional property: %w", err))
				}
			}
		}

		return nil
	}

	return errors.New("invalid type expression")
}

func annotateBuiltinSchemaProvenanceRuntime(value *RuntimeValue) {
	if value == nil {
		return
	}
	contents := runtimeNodeContentsPointer(value)
	if contents == nil {
		return
	}
	//nolint:exhaustive // 部分的なKind判定であり、その他の値は後続の共通処理で扱います。
	switch contents.Kind() {
	case Map:
		apiVersion, hasAPIVersion := runtimeStringField(*contents, "apiVersion")
		kind, hasKind := runtimeStringField(*contents, "kind")
		if hasAPIVersion && hasKind {
			if schemaType, exists := builtinKubernetesSchemaType(apiVersion, kind); exists {
				annotateRuntimeSchemaProvenance(value, apiVersion+"/"+kind, "", &schemaType)

				return
			}
			annotateUnknownRuntimeSchemaProvenance(value, apiVersion+"/"+kind)
		}
		for index := range contents.Map {
			annotateBuiltinSchemaProvenanceRuntime(&contents.Map[index].Value)
		}
	case List, AdapterBundle:
		for index := range contents.List {
			annotateBuiltinSchemaProvenanceRuntime(&contents.List[index])
		}
	}
}

func annotateUnknownRuntimeSchemaProvenance(value *RuntimeValue, resourceType string) {
	for current := value; current != nil; current = current.Base {
		current.EnsureTyping().SchemaProvenance = &SchemaProvenance{ResourceType: resourceType, Unknown: true}
		if current.Kind() != Node {
			break
		}
	}
}

func annotateRuntimeSchemaProvenance(value *RuntimeValue, resourceType, path string, schemaType *ResolvedType) {
	if value == nil || schemaType == nil {
		return
	}
	for current := value; current != nil; current = current.Base {
		current.EnsureTyping().SchemaProvenance = &SchemaProvenance{ResourceType: resourceType, Path: path, Type: schemaType}
		if current.Kind() != Node {
			break
		}
	}
	contents := runtimeNodeContentsPointer(value)
	if contents == nil {
		return
	}
	//nolint:exhaustive // 部分的なKind判定であり、その他の値は後続の共通処理で扱います。
	switch contents.Kind() {
	case Map:
		for index := range contents.Map {
			entry := &contents.Map[index]
			fieldType, exists := schemaFieldType(*schemaType, entry.Key)
			if !exists {
				fieldType = new(ResolvedType{Name: typeAny})
			}
			fieldPath := entry.Key
			if path != "" {
				fieldPath = path + "." + fieldPath
			}
			annotateRuntimeSchemaProvenance(&entry.Value, resourceType, fieldPath, fieldType)
		}
	case List, AdapterBundle:
		elementType, exists := schemaElementType(*schemaType)
		if !exists {
			unknownType := ResolvedType{Name: typeAny}
			elementType = &unknownType
		}
		itemPath := "[]"
		if path != "" {
			itemPath = path + "[]"
		}
		for index := range contents.List {
			annotateRuntimeSchemaProvenance(&contents.List[index], resourceType, itemPath, elementType)
		}
	}
}

func runtimeNodeContentsPointer(value *RuntimeValue) *RuntimeValue {
	for value != nil && value.Kind() == Node && value.Base != nil {
		value = value.Base
	}

	return value
}

func runtimeStringField(value RuntimeValue, key string) (string, bool) {
	field, exists := runtimeNodeContents(value).MapLookup(key)
	if !exists || field.Kind() != String {
		return "", false
	}

	return field.TextValue(), true
}

func requireRuntimeKind(value RuntimeValue, kind Kind, typeName string) error {
	if value.Kind() != kind {
		return fmt.Errorf("expected %s, got %s", typeName, kindName(value.Kind()))
	}

	return nil
}

func runtimeNodeContents(value RuntimeValue) RuntimeValue {
	for value.Kind() == Node && value.Base != nil {
		value = *value.Base
	}

	return value
}

func runtimeMapKeyDiagnostic(entry RuntimeEntry, err error) error {
	locations := appendSourceLocations(nil, entry.KeySourceLocations, entry.KeySourcePath, entry.KeySpan, "Map key")
	if len(locations) == 0 {
		return err
	}
	primary := locations[len(locations)-1]
	code := codeMCLType
	var unknownField unknownTypeField
	if errors.As(err, &unknownField) {
		code = "E201"
	}

	diagnostic := SourceDiagnosticError{Path: primary.Path, Code: code, Message: err.Error(), Span: primary.Span, Related: locations[:len(locations)-1], Cause: err}
	if unknownField.suggestion != "" {
		diagnostic.Message = fmt.Sprintf("unknown field %q; did you mean %q?", unknownField.name, unknownField.suggestion)
	}

	return diagnostic
}

func addTypeDiagnosticContext(err error, typeName, schemaPath string) error {
	diagnostic, ok := SourceDiagnosticFromError(err)
	if !ok || diagnostic.Code != "E201" {
		return err
	}
	if diagnostic.TypeName == "" {
		diagnostic.TypeName = typeName
	}
	if diagnostic.SchemaPath == "" {
		diagnostic.SchemaPath = schemaPath
	}

	return diagnostic
}

func runtimeValueAsJSON(value RuntimeValue) (any, error) {
	value = runtimeNodeContents(value)
	switch value.Kind() {
	case Null:
		return nil, nil
	case Bool:
		return value.BoolValue(), nil
	case Integer, Decimal:
		if value.Kind() == Integer {
			if _, err := parseRuntimeInteger(value.TextValue()); err != nil {
				return nil, fmt.Errorf("invalid Integer value %q", value.TextValue())
			}
		} else {
			parsed, err := parseRuntimeDecimal(value.TextValue())
			if err != nil {
				return nil, fmt.Errorf("invalid Decimal value %q: %w", value.TextValue(), err)
			}
			if err := validateRuntimeDecimal(parsed); err != nil {
				return nil, err
			}
		}

		return json.Number(value.TextValue()), nil
	case String:
		return value.TextValue(), nil
	case List, AdapterBundle:
		result := make([]any, len(value.List))
		for index, item := range value.List {
			converted, err := runtimeValueAsJSON(item)
			if err != nil {
				return nil, fmt.Errorf("item %d: %w", index, err)
			}
			result[index] = converted
		}

		return result, nil
	case Map:
		result := make(map[string]any, len(value.Map))
		for _, entry := range value.Map {
			converted, err := runtimeValueAsJSON(entry.Value)
			if err != nil {
				return nil, fmt.Errorf("field %q: %w", entry.Key, err)
			}
			result[entry.Key] = converted
		}

		return result, nil
	default:
		return nil, fmt.Errorf("cannot validate %s with a JSON Schema", kindName(value.Kind()))
	}
}

func schemaValidationRuntimeDiagnostic(value RuntimeValue, err error) error {
	validation, ok := errors.AsType[*jsonschema.ValidationError](err)
	if !ok {
		return err
	}
	if multiple := schemaValidationMultipleCause(validation); multiple != nil {
		if diagnostic, ok := schemaValidationRuntimeMultipleDiagnostic(value, multiple, err); ok {
			return diagnostic
		}
	}
	validation = schemaValidationCause(validation)
	current := runtimeNodeContents(value)
	var related []SourceDiagnosticLocation
	for _, part := range validation.InstanceLocation {
		current = runtimeNodeContents(current)
		metadata := current.Metadata()
		switch current.Kind() {
		case Map:
			related = appendSourceLocations(related, metadata.SourceLocations, metadata.SourcePath, metadata.Span, "Containing map")
			child, exists := current.MapLookup(part)
			if !exists {
				return err
			}
			current = child
		case List, AdapterBundle:
			related = appendSourceLocations(related, metadata.SourceLocations, metadata.SourcePath, metadata.Span, "Containing list")
			index, parseErr := strconv.Atoi(part)
			if parseErr != nil || index < 0 || index >= len(current.List) {
				return err
			}
			current = current.List[index]
		default:
			return err
		}
	}
	if schemaValidationTargetsUnknownProperty(validation) && current.Kind() == Map {
		additional, _ := validation.ErrorKind.(*jsonschemakind.AdditionalProperties)
		if additional != nil && len(additional.Properties) == 1 {
			if entry, exists := runtimeMapEntry(current, additional.Properties[0]); exists {
				metadata := current.Metadata()
				related = appendSourceLocations(related, metadata.SourceLocations, metadata.SourcePath, metadata.Span, "Containing map")
				keyLocations := appendSourceLocations(nil, entry.KeySourceLocations, entry.KeySourcePath, entry.KeySpan, "Map key")
				if len(keyLocations) > 0 {
					primary := keyLocations[len(keyLocations)-1]
					related = appendSourceLocations(related, keyLocations[:len(keyLocations)-1], "", SourceSpan{}, "")
					code := codeMCLType
					if schemaValidationTargetsUnknownProperty(validation) {
						code = "E201"
					}

					return SourceDiagnosticError{Path: primary.Path, Code: code, Message: err.Error(), Span: primary.Span, Related: withoutSourceLocation(related, primary.Path, primary.Span), Cause: err}
				}
			}
		}
	}
	metadata := current.Metadata()
	if metadata.SourcePath != "" && metadata.Span.End > metadata.Span.Start {
		related = appendSourceLocations(related, metadata.SourceLocations, metadata.SourcePath, metadata.Span, "Invalid value")

		return SourceDiagnosticError{Path: metadata.SourcePath, Code: codeMCLType, Message: err.Error(), Span: metadata.Span, Related: withoutSourceLocation(related, metadata.SourcePath, metadata.Span), Cause: err}
	}
	if len(related) > 0 {
		return SourceDiagnosticError{Path: related[0].Path, Code: codeMCLType, Message: err.Error(), Span: related[0].Span, Related: related[1:], Cause: err}
	}

	return err
}

func schemaValidationRuntimeMultipleDiagnostic(value RuntimeValue, validation *jsonschema.ValidationError, err error) (error, bool) {
	var leaves []*jsonschema.ValidationError
	var collect func(*jsonschema.ValidationError)
	collect = func(current *jsonschema.ValidationError) {
		if len(current.Causes) == 0 {
			leaves = append(leaves, current)

			return
		}
		for _, cause := range current.Causes {
			collect(cause)
		}
	}
	collect(validation)
	var primary SourceDiagnosticLocation
	var related []SourceDiagnosticLocation
	for _, leaf := range leaves {
		if schemaValidationTargetsUnknownProperty(leaf) {
			return nil, false
		}
		current := runtimeNodeContents(value)
		var containing []SourceDiagnosticLocation
		pathValid := true
		for _, part := range leaf.InstanceLocation {
			current = runtimeNodeContents(current)
			metadata := current.Metadata()
			switch current.Kind() {
			case Map:
				containing = appendSourceLocations(containing, metadata.SourceLocations, metadata.SourcePath, metadata.Span, "Containing map")
				child, exists := current.MapLookup(part)
				if !exists {
					pathValid = false

					break
				}
				current = child
			case List, AdapterBundle:
				containing = appendSourceLocations(containing, metadata.SourceLocations, metadata.SourcePath, metadata.Span, "Containing list")
				index, parseErr := strconv.Atoi(part)
				if parseErr != nil || index < 0 || index >= len(current.List) {
					pathValid = false

					break
				}
				current = current.List[index]
			default:
				pathValid = false
			}
			if !pathValid {
				break
			}
		}
		if !pathValid {
			continue
		}
		metadata := current.Metadata()
		if metadata.SourcePath == "" || metadata.Span.End <= metadata.Span.Start {
			continue
		}
		location := SourceDiagnosticLocation{Path: metadata.SourcePath, Span: metadata.Span, Message: "Invalid value"}
		if primary.Path == "" {
			primary = location
		}
		related = appendSourceLocations(related, containing, "", SourceSpan{}, "")
		related = appendSourceLocations(related, metadata.SourceLocations, metadata.SourcePath, metadata.Span, "Invalid value")
	}
	if primary.Path == "" {
		return nil, false
	}

	return SourceDiagnosticError{Path: primary.Path, Code: codeMCLType, Message: err.Error(), Span: primary.Span, Related: withoutSourceLocation(related, primary.Path, primary.Span), Cause: err}, true
}

func runtimeMapEntry(value RuntimeValue, key string) (RuntimeEntry, bool) {
	for _, entry := range value.Map {
		if entry.Key == key {
			return entry, true
		}
	}

	return RuntimeEntry{}, false
}

func typeProvenanceCoveredByUnion(origin TypeProvenance, provenance []TypeProvenance) bool {
	for _, candidate := range provenance {
		if equivalentTypeProvenance(origin, candidate.Type, candidate.Types) {
			continue
		}
		resolved, known := resolveStaticAlias(candidate.Type, candidate.Types, make(map[string]bool))
		if !known {
			continue
		}
		for _, option := range resolved.Options {
			if equivalentTypeProvenance(origin, option, candidate.Types) {
				return true
			}
		}
	}

	return false
}

func inheritRuntimeSchemaProvenance(value *RuntimeValue, base, patch RuntimeValue) {
	var provenance *SchemaProvenance
	if base.Metadata().Typing != nil {
		provenance = base.Metadata().Typing.SchemaProvenance
	}
	if provenance == nil {
		if patch.Metadata().Typing != nil {
			provenance = patch.Metadata().Typing.SchemaProvenance
		}
	}
	if provenance == nil {
		return
	}
	annotateRuntimeSchemaProvenance(value, provenance.ResourceType, provenance.Path, provenance.Type)
}

func equivalentTypeProvenance(origin TypeProvenance, typeExpr ResolvedType, types map[string]ResolvedType) bool {
	if origin.Type.SchemaID != "" || typeExpr.SchemaID != "" {
		return origin.Type.SchemaID != "" && origin.Type.SchemaID == typeExpr.SchemaID
	}
	if origin.Type.SchemaPath != "" || typeExpr.SchemaPath != "" {
		return origin.Type.SchemaPath != "" && origin.Type.SchemaPath == typeExpr.SchemaPath
	}
	left, leftKnown := resolveStaticAlias(origin.Type, origin.Types, make(map[string]bool))
	right, rightKnown := resolveStaticAlias(typeExpr, types, make(map[string]bool))

	return leftKnown && rightKnown && equalStaticTypes(left, right)
}

func schemaValidationMultipleCause(validation *jsonschema.ValidationError) *jsonschema.ValidationError {
	if len(validation.Causes) > 1 {
		return validation
	}
	if len(validation.Causes) == 1 {
		return schemaValidationMultipleCause(validation.Causes[0])
	}

	return nil
}

func appendSourceLocations(locations, additions []SourceDiagnosticLocation, path string, span SourceSpan, message string) []SourceDiagnosticLocation {
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
	for _, location := range additions {
		appendLocation(location)
	}
	appendLocation(SourceDiagnosticLocation{Path: path, Span: span, Message: message})

	return locations
}

func withoutSourceLocation(locations []SourceDiagnosticLocation, path string, span SourceSpan) []SourceDiagnosticLocation {
	return slices.DeleteFunc(locations, func(location SourceDiagnosticLocation) bool {
		return location.Path == path && location.Span == span
	})
}

func schemaValidationCause(validation *jsonschema.ValidationError) *jsonschema.ValidationError {
	for len(validation.Causes) == 1 {
		validation = validation.Causes[0]
	}

	return validation
}

func schemaValidationTargetsUnknownProperty(validation *jsonschema.ValidationError) bool {
	_, ok := validation.ErrorKind.(*jsonschemakind.AdditionalProperties)

	return ok
}

func validateTypeExpr(typeExpr ResolvedType, types map[string]ResolvedType, resolving map[string]bool) error {
	return validateTypeExprWithParameters(typeExpr, types, resolving, nil)
}

func validateTypeExprWithParameters(typeExpr ResolvedType, types map[string]ResolvedType, resolving map[string]bool, parameters map[string]bool) error {
	if typeExpr.Name != "" {
		if parameters[typeExpr.Name] && len(typeExpr.Arguments) == 0 {
			return nil
		}
		switch typeExpr.Name {
		case typeAny, typeNull, typeBool, typeInt, typeDecimal, typeString:
		case typeList:
			if typeExpr.Element == nil {
				return errors.New("list requires an element type")
			}
		case typeMap:
			if typeExpr.Fields == nil && !typeExpr.OpenRecord && typeExpr.Element == nil {
				return errors.New("map requires a value type")
			}
		case typeNode:
			if len(typeExpr.Arguments) != 1 {
				return errors.New("node requires a value type")
			}
		case typeFunction:
			if typeExpr.ReturnType == nil || len(typeExpr.Parameters) == 0 {
				return errors.New("function requires parameter and return types")
			}
		default:
			definition, exists := types[typeExpr.Name]
			if !exists {
				return fmt.Errorf("unknown type %q", typeExpr.Name)
			}
			if len(typeExpr.Arguments) != len(definition.TypeParameters) {
				return fmt.Errorf("%s expects %d type arguments", typeExpr.Name, len(definition.TypeParameters))
			}
			if !resolving[typeExpr.Name] {
				resolving[typeExpr.Name] = true
				if err := validateTypeExprWithParameters(definition, types, resolving, parameterSet(definition.TypeParameters)); err != nil {
					return err
				}
				delete(resolving, typeExpr.Name)
			}
		}
	}
	for _, argument := range typeExpr.Arguments {
		if err := validateTypeExprWithParameters(argument, types, resolving, parameters); err != nil {
			return err
		}
	}
	for _, option := range typeExpr.Options {
		if err := validateTypeExprWithParameters(option, types, resolving, parameters); err != nil {
			return err
		}
	}
	if typeExpr.Element != nil {
		if err := validateTypeExprWithParameters(*typeExpr.Element, types, resolving, parameters); err != nil {
			return err
		}
	}
	for _, parameter := range typeExpr.Parameters {
		if err := validateTypeExprWithParameters(parameter.Type, types, resolving, parameters); err != nil {
			return fmt.Errorf("parameter %q: %w", parameter.Name, err)
		}
	}
	if typeExpr.ReturnType != nil {
		if err := validateTypeExprWithParameters(*typeExpr.ReturnType, types, resolving, parameters); err != nil {
			return fmt.Errorf("return type: %w", err)
		}
	}
	for _, field := range typeExpr.Fields {
		if err := validateTypeExprWithParameters(field.Type, types, resolving, parameters); err != nil {
			return fmt.Errorf("field %q: %w", field.Name, err)
		}
	}
	if typeExpr.AdditionalProperties != nil {
		if err := validateTypeExprWithParameters(*typeExpr.AdditionalProperties, types, resolving, parameters); err != nil {
			return fmt.Errorf("additional properties: %w", err)
		}
	}

	return nil
}

func parameterSet(parameters []string) map[string]bool {
	set := make(map[string]bool, len(parameters))
	for _, parameter := range parameters {
		set[parameter] = true
	}

	return set
}

func instantiateTypeAlias(reference, definition ResolvedType) (ResolvedType, error) {
	if len(reference.Arguments) != len(definition.TypeParameters) {
		return ResolvedType{}, fmt.Errorf("%s expects %d type arguments", reference.Name, len(definition.TypeParameters))
	}
	bindings := make(map[string]ResolvedType, len(reference.Arguments))
	for index, parameter := range definition.TypeParameters {
		bindings[parameter] = reference.Arguments[index]
	}
	definition.TypeParameters = nil

	return substituteTypeParameters(definition, bindings), nil
}

func substituteTypeParameters(typeExpr ResolvedType, bindings map[string]ResolvedType) ResolvedType {
	if replacement, exists := bindings[typeExpr.Name]; exists && len(typeExpr.Arguments) == 0 {
		return replacement
	}
	typeExpr.Options = slices.Clone(typeExpr.Options)
	for index := range typeExpr.Options {
		typeExpr.Options[index] = substituteTypeParameters(typeExpr.Options[index], bindings)
	}
	typeExpr.Arguments = slices.Clone(typeExpr.Arguments)
	for index := range typeExpr.Arguments {
		typeExpr.Arguments[index] = substituteTypeParameters(typeExpr.Arguments[index], bindings)
	}
	if typeExpr.Element != nil {
		element := substituteTypeParameters(*typeExpr.Element, bindings)
		typeExpr.Element = &element
	}
	typeExpr.Fields = slices.Clone(typeExpr.Fields)
	for index := range typeExpr.Fields {
		typeExpr.Fields[index].Type = substituteTypeParameters(typeExpr.Fields[index].Type, bindings)
	}
	if typeExpr.AdditionalProperties != nil {
		additionalProperties := substituteTypeParameters(*typeExpr.AdditionalProperties, bindings)
		typeExpr.AdditionalProperties = &additionalProperties
	}
	typeExpr.Parameters = slices.Clone(typeExpr.Parameters)
	for index := range typeExpr.Parameters {
		typeExpr.Parameters[index].Type = substituteTypeParameters(typeExpr.Parameters[index].Type, bindings)
	}
	if typeExpr.ReturnType != nil {
		returnType := substituteTypeParameters(*typeExpr.ReturnType, bindings)
		typeExpr.ReturnType = &returnType
	}

	return typeExpr
}
