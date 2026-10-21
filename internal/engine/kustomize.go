package engine

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/manifest-configuration-language/mcl/internal/kustomizeadapter"
)

type kustomizeBuildInput struct {
	source    string
	revision  RuntimeValue
	directory string
}

func kustomizeBuild(arguments []RuntimeValue, state *EvaluationState) (RuntimeValue, error) {
	projectRoot, sourcePath, dependencies := adapterModuleInputs(state, arguments...)
	var files *projectFilesystem
	if state != nil && state.Module != nil {
		files = state.Module.filesystem
	}
	if files == nil && projectRoot != "" {
		files = newOSProjectFilesystem(projectRoot)
	}
	input, err := validateKustomizeBuildInput(arguments, projectRoot, sourcePath)
	if err != nil {
		return RuntimeValue{}, err
	}
	if filepath.IsAbs(input.directory) {
		return RuntimeValue{}, errors.New("kustomize directory must be relative to the source")
	}
	if strings.HasPrefix(input.source, "git+") {
		if input.revision.Kind() != String || input.revision.TextValue() == "" {
			return RuntimeValue{}, errors.New("kustomize Git source requires a full commit SHA-1 revision")
		}
	} else if input.revision.Kind() != String || input.revision.TextValue() != "" {
		return RuntimeValue{}, errors.New("local Kustomize source requires an empty revision")
	}
	result, err := kustomizeadapter.Build(kustomizeadapter.Request{Source: input.source, Revision: input.revision.TextValue(), Directory: input.directory, ProjectRoot: projectRoot, SourcePath: sourcePath, Dependencies: dependencies})
	if err != nil {
		if diagnostic, ok := kustomizeConfigSourceDiagnostic(err, files); ok {
			return RuntimeValue{}, diagnostic
		}

		return RuntimeValue{}, err
	}
	allocations := runtimeAllocationBudgetOf(state)
	value, err := decodeRuntimeYAMLDocuments(result.YAML, "", allocations)
	if err != nil {
		return RuntimeValue{}, err
	}

	return mapKustomizeBuildOrigins(value, result, projectRoot, allocations)
}

func validateKustomizeBuildInput(arguments []RuntimeValue, projectRoot, sourcePath string) (kustomizeBuildInput, error) {
	if len(arguments) != 3 || arguments[0].Kind() != String || arguments[2].Kind() != String {
		return kustomizeBuildInput{}, errors.New("kustomize.build expects source, revision, and directory Strings")
	}
	if projectRoot == "" || sourcePath == "" {
		return kustomizeBuildInput{}, errors.New("kustomize.build requires file-based evaluation")
	}
	input := kustomizeBuildInput{source: arguments[0].TextValue(), revision: arguments[1], directory: arguments[2].TextValue()}
	if input.directory == "" {
		return kustomizeBuildInput{}, errors.New("kustomize directory is required")
	}

	return input, nil
}

func mapKustomizeBuildOrigins(value RuntimeValue, result kustomizeadapter.Result, projectRoot string, allocations *runtimeAllocationBudget) (RuntimeValue, error) {
	if value.Kind() == AdapterBundle && len(value.ListItems()) == len(result.Origins) {
		artifacts, err := mapKustomizeResourceArtifacts(value, result, projectRoot, allocations)
		if err != nil {
			return RuntimeValue{}, err
		}

		return runtimeBundleValueWithBudget(artifacts, "", allocations)
	}
	lifecycle := Lifecycle{Events: []string{phaseMain}, Origin: result.Origin}

	return runtimeBundleValueWithBudget([]RuntimeArtifact{{Value: value, Lifecycle: lifecycle}}, "", allocations)
}

func mapKustomizeResourceArtifacts(value RuntimeValue, result kustomizeadapter.Result, projectRoot string, allocations *runtimeAllocationBudget) ([]RuntimeArtifact, error) {
	artifacts := make([]RuntimeArtifact, len(result.Origins))
	for index, origin := range result.Origins {
		if err := mapKustomizeResourceProvenance(&value.List[index], origin, result, allocations); err != nil {
			return nil, err
		}
		lifecycle := Lifecycle{Events: []string{phaseMain}, Origin: kustomizeResourceLocator(result.Origin, origin, result.LocalSourceRoot, projectRoot, result.SourceDirectory)}
		artifacts[index] = RuntimeArtifact{Value: value.List[index], Lifecycle: lifecycle}
	}

	return artifacts, nil
}

func mapKustomizeResourceProvenance(output *RuntimeValue, origin kustomizeadapter.ResourceOrigin, result kustomizeadapter.Result, allocations *runtimeAllocationBudget) error {
	path := origin.Path
	if path == "" {
		path = origin.ConfiguredIn
	}
	if path == "" {
		return nil
	}
	var sourceData []byte
	var resourcePath string
	var ok bool
	if origin.Repo != "" {
		path, ok = canonicalKustomizeResourcePath(path)
		if !ok {
			return nil
		}
		resourcePath = "kustomize:" + strings.TrimPrefix(origin.Repo, "git+") + "@" + origin.Ref + "/" + path
		sourceData, ok = result.SourceLocators[resourcePath]
	} else {
		relative := filepath.ToSlash(filepath.Join(result.SourceDirectory, filepath.FromSlash(path)))
		sourceData, ok = result.SourceFiles[relative]
		resourcePath = filepath.Join(result.SourceRoot, filepath.FromSlash(relative))
	}
	if !ok {
		return nil
	}
	sourceDocuments, err := decodeRuntimeYAMLDocuments(sourceData, resourcePath, allocations)
	if err != nil {
		if errors.Is(err, errRuntimeAllocationLimit) {
			return err
		}

		return nil
	}
	if sourceDocument, ok := kustomizeSourceDocument(*output, sourceDocuments.ListItems()); ok {
		transferKustomizeSourceProvenance(output, sourceDocument)
	}

	return nil
}

func kustomizeResourceLocator(fallback string, origin kustomizeadapter.ResourceOrigin, localSourceDir, projectRoot, directory string) string {
	path := origin.Path
	if path == "" {
		path = origin.ConfiguredIn
	}
	if path == "" {
		return fallback
	}
	if origin.Repo != "" {
		path, ok := canonicalKustomizeResourcePath(path)
		if !ok {
			return fallback
		}

		return fmt.Sprintf("kustomize:%s@%s/%s", strings.TrimPrefix(origin.Repo, "git+"), origin.Ref, path)
	}
	if localSourceDir == "" {
		return fallback + "/" + filepath.ToSlash(path)
	}
	resolved, ok := kustomizeResourceSourcePath(origin, localSourceDir, directory)
	if !ok {
		return fallback
	}
	relativePath, err := filepath.Rel(projectRoot, resolved)
	if err != nil {
		return fallback
	}

	return "kustomize:" + filepath.ToSlash(relativePath)
}

func canonicalKustomizeResourcePath(resourcePath string) (string, bool) {
	resourcePath = filepath.ToSlash(resourcePath)
	cleaned := path.Clean(resourcePath)
	if cleaned == "." || cleaned == ".." || path.IsAbs(cleaned) || strings.HasPrefix(cleaned, "../") {
		return "", false
	}

	return cleaned, true
}

func kustomizeResourceSourcePath(origin kustomizeadapter.ResourceOrigin, localSourceDir, directory string) (string, bool) {
	if localSourceDir == "" {
		return "", false
	}
	path := origin.Path
	if path == "" {
		path = origin.ConfiguredIn
	}
	if path == "" || origin.Repo != "" {
		return "", false
	}
	sourcePath := filepath.Join(localSourceDir, filepath.FromSlash(directory), filepath.FromSlash(path))
	resolved, err := filepath.EvalSymlinks(sourcePath)
	if err != nil || !pathWithin(localSourceDir, resolved) {
		return "", false
	}

	return resolved, true
}

func kustomizeSourceDocument(output RuntimeValue, candidates []RuntimeValue) (RuntimeValue, bool) {
	var exact RuntimeValue
	var exactCount int
	var sameKind RuntimeValue
	var sameKindCount int
	for _, candidate := range candidates {
		if kustomizeResourceIdentity(output, "apiVersion") != "" && kustomizeResourceIdentity(output, "apiVersion") == kustomizeResourceIdentity(candidate, "apiVersion") &&
			kustomizeResourceIdentity(output, "kind") == kustomizeResourceIdentity(candidate, "kind") {
			sameKind = candidate
			sameKindCount++
			if kustomizeResourceIdentity(output, "metadata.name") == kustomizeResourceIdentity(candidate, "metadata.name") &&
				kustomizeResourceIdentity(output, "metadata.namespace") == kustomizeResourceIdentity(candidate, "metadata.namespace") {
				exact = candidate
				exactCount++
			}
		}
	}
	if exactCount == 1 {
		return exact, true
	}
	if sameKindCount == 1 {
		return sameKind, true
	}

	return RuntimeValue{}, false
}

func kustomizeResourceIdentity(value RuntimeValue, path string) string {
	for field := range strings.SplitSeq(path, ".") {
		next, exists := value.MapLookup(field)
		if !exists {
			return ""
		}
		value = next
	}
	if value.Kind() == String {
		return value.TextValue()
	}

	return ""
}

func transferKustomizeSourceProvenance(target *RuntimeValue, source RuntimeValue) {
	location := SourceDiagnosticLocation{Path: source.Value.SourcePath, Span: source.Value.Span, Message: kustomizeSourceLocationLabel}
	if target.Kind() != source.Kind() {
		location.Message = kustomizeTransformedSourceLocationLabel
		location.Origin = SourceOriginKustomizeChange
		setKustomizeSourceLocation(target, location)

		return
	}
	if equalKustomizeRuntimeValues(*target, source) {
		target.Value.SourcePath = source.Value.SourcePath
		target.Value.Span = source.Value.Span
	} else {
		location.Message = kustomizeTransformedSourceLocationLabel
		location.Origin = SourceOriginKustomizeChange
		setKustomizeTransformedRoot(target, source, location)
	}
	switch target.Kind() {
	case Map:
		transferKustomizeMapProvenance(target, source, location)
	case List, AdapterBundle:
		transferKustomizeListProvenance(target, source, location)
	default:
		if !equalKustomizeRuntimeValues(*target, source) {
			target.Value.SourcePath = ""
			target.Value.Span = SourceSpan{}
		}
	}
}

func setKustomizeTransformedRoot(target *RuntimeValue, source RuntimeValue, location SourceDiagnosticLocation) {
	target.Value.SourcePath = ""
	target.Value.Span = SourceSpan{}
	target.Value.SourceLocations = appendSourceLocations(target.Value.SourceLocations, []SourceDiagnosticLocation{location}, "", SourceSpan{}, "")
	if (target.Kind() == List || target.Kind() == AdapterBundle) && len(target.List) != len(source.List) {
		for index := range target.List {
			setKustomizeSourceLocation(&target.List[index], location)
		}
	}
}

func transferKustomizeMapProvenance(target *RuntimeValue, source RuntimeValue, location SourceDiagnosticLocation) {
	for index := range target.Map {
		sourceIndex := slices.IndexFunc(source.Map, func(entry RuntimeEntry) bool { return entry.Key == target.Map[index].Key })
		if sourceIndex < 0 {
			target.Map[index].KeySourceLocations = appendSourceLocations(target.Map[index].KeySourceLocations, []SourceDiagnosticLocation{location}, "", SourceSpan{}, "")
			setKustomizeSourceLocation(&target.Map[index].Value, location)

			continue
		}
		target.Map[index].KeySourcePath = source.Map[sourceIndex].KeySourcePath
		target.Map[index].KeySpan = source.Map[sourceIndex].KeySpan
		target.Map[index].KeySourceLocations = slices.Clone(source.Map[sourceIndex].KeySourceLocations)
		transferKustomizeSourceProvenance(&target.Map[index].Value, source.Map[sourceIndex].Value)
	}
}

func transferKustomizeListProvenance(target *RuntimeValue, source RuntimeValue, location SourceDiagnosticLocation) {
	if len(target.List) != len(source.List) {
		return
	}
	sourceIndexes := make(map[string]int, len(source.List))
	for index, value := range source.List {
		fingerprint, ok := kustomizeValueFingerprint(value, 0)
		if !ok {
			continue
		}
		if _, exists := sourceIndexes[fingerprint]; exists {
			sourceIndexes[fingerprint] = -1

			continue
		}
		sourceIndexes[fingerprint] = index
	}
	usedSources := make([]bool, len(source.List))
	for index := range target.List {
		fingerprint, ok := kustomizeValueFingerprint(target.List[index], 0)
		sourceIndex, exists := sourceIndexes[fingerprint]
		if !ok || !exists || sourceIndex < 0 || usedSources[sourceIndex] || !equalKustomizeRuntimeValues(target.List[index], source.List[sourceIndex]) {
			setKustomizeSourceLocation(&target.List[index], location)

			continue
		}
		usedSources[sourceIndex] = true
		transferKustomizeSourceProvenance(&target.List[index], source.List[sourceIndex])
	}
}

func equalKustomizeRuntimeValues(left, right RuntimeValue) bool {
	leftFingerprint, leftOK := kustomizeValueFingerprint(left, 0)
	rightFingerprint, rightOK := kustomizeValueFingerprint(right, 0)

	return leftOK && rightOK && leftFingerprint == rightFingerprint
}

func kustomizeValueFingerprint(value RuntimeValue, depth int) (string, bool) {
	if depth > maxYAMLDepth {
		return "", false
	}
	var fingerprint strings.Builder
	switch value.Kind() {
	case Null:
		fingerprint.WriteString("null")
	case Bool:
		fingerprint.WriteString("bool:")
		fingerprint.WriteString(strconv.FormatBool(value.BoolValue()))
	case Integer, Decimal, String:
		fingerprint.WriteString(strconv.Itoa(int(value.Kind())))
		fingerprint.WriteByte(':')
		fingerprint.WriteString(strconv.Quote(value.TextValue()))
	case List, AdapterBundle:
		fingerprint.WriteString("list:")
		fingerprint.WriteString(strconv.Itoa(len(value.ListItems())))
		for _, item := range value.ListItems() {
			child, ok := kustomizeValueFingerprint(item, depth+1)
			if !ok {
				return "", false
			}
			fingerprint.WriteByte(':')
			writeKustomizeFingerprintPart(&fingerprint, child)
		}
	case Map:
		fingerprint.WriteString("map:")
		fingerprint.WriteString(strconv.Itoa(len(value.Entries())))
		indices := make([]int, len(value.Entries()))
		for index := range indices {
			indices[index] = index
		}
		slices.SortFunc(indices, func(left, right int) int {
			return strings.Compare(value.Map[left].Key, value.Map[right].Key)
		})
		for _, index := range indices {
			child, ok := kustomizeValueFingerprint(value.Map[index].Value, depth+1)
			if !ok {
				return "", false
			}
			fingerprint.WriteByte(':')
			writeKustomizeFingerprintPart(&fingerprint, value.Map[index].Key)
			fingerprint.WriteByte('=')
			writeKustomizeFingerprintPart(&fingerprint, child)
		}
	default:
		return "", false
	}

	return fingerprint.String(), true
}

func writeKustomizeFingerprintPart(fingerprint *strings.Builder, value string) {
	fingerprint.WriteString(strconv.Itoa(len(value)))
	fingerprint.WriteByte(':')
	fingerprint.WriteString(value)
}

func setKustomizeSourceLocation(value *RuntimeValue, location SourceDiagnosticLocation) {
	if value == nil {
		return
	}
	value.Value.SourceLocations = appendSourceLocations(value.Value.SourceLocations, []SourceDiagnosticLocation{location}, "", SourceSpan{}, "")
	for index := range value.List {
		setKustomizeSourceLocation(&value.List[index], location)
	}
	for index := range value.Map {
		value.Map[index].KeySourceLocations = appendSourceLocations(value.Map[index].KeySourceLocations, []SourceDiagnosticLocation{location}, "", SourceSpan{}, "")
		setKustomizeSourceLocation(&value.Map[index].Value, location)
	}
}

func kustomizeConfigSourceDiagnostic(err error, filesystems ...*projectFilesystem) (SourceDiagnosticError, bool) {
	configError, ok := errors.AsType[kustomizeadapter.ConfigSyntaxError](err)
	if !ok || configError.Line <= 0 || configError.SourcePath == "" {
		return SourceDiagnosticError{}, false
	}
	for part := range strings.SplitSeq(filepath.ToSlash(configError.SourcePath), "/") {
		if strings.HasPrefix(part, ".mcl-remote-") {
			return SourceDiagnosticError{}, false
		}
	}
	path := configError.SourcePath
	data := configError.SourceData
	if data == nil {
		filesystem := projectFilesystemFor(filepath.Dir(path), filesystems)
		var readErr error
		data, readErr = filesystem.readLimitedFile(path)
		if readErr != nil {
			return SourceDiagnosticError{}, false
		}
	}
	span, ok := sourceLineSpan(data, configError.Line)
	if !ok {
		return SourceDiagnosticError{}, false
	}

	return SourceDiagnosticError{
		Path:    path,
		Code:    "MCL-KUSTOMIZE",
		Message: fmt.Sprintf("parse Kustomize configuration %q: %v", path, configError.Cause),
		Span:    span,
		Cause:   configError,
	}, true
}
