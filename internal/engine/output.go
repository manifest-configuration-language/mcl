package engine

import (
	"fmt"

	kubernetespkg "github.com/manifest-configuration-language/mcl/internal/kubernetes"
	outputpkg "github.com/manifest-configuration-language/mcl/internal/output"
)

func Render(bundle Bundle) ([]byte, error) { return RenderPhase(bundle, phaseMain) }

func RenderWithSourceMap(bundle Bundle) ([]byte, ManifestSourceMap, error) {
	return RenderPhaseWithSourceMap(bundle, phaseMain)
}

func ValidateBundleOutput(bundle Bundle) error { return NewBundleOutputPlan(bundle).Validate() }

func ValidateBundleOutputWithCatalog(bundle Bundle, catalog KubernetesResourceCatalog) error {
	plan, err := NewBundleOutputPlanWithCatalog(bundle, catalog)
	if err != nil {
		return err
	}
	return plan.Validate()
}

func RenderPhase(bundle Bundle, phase string) ([]byte, error) {
	if !validLifecycleEvent(phase) {
		return nil, fmt.Errorf("unknown output phase %q", phase)
	}
	return NewBundleOutputPlan(bundle).RenderPhase(phase)
}

func RenderPhaseWithSourceMap(bundle Bundle, phase string) ([]byte, ManifestSourceMap, error) {
	if !validLifecycleEvent(phase) {
		return nil, ManifestSourceMap{}, fmt.Errorf("unknown output phase %q", phase)
	}
	return NewBundleOutputPlan(bundle).RenderPhaseWithSourceMap(phase)
}

type BundleOutputPlan = outputpkg.BundleOutputPlan

type RenderedFile = outputpkg.RenderedFile
type RenderedArtifact = outputpkg.RenderedArtifact

type indexedArtifact struct {
	index    int
	artifact Artifact
}

type kubernetesResourceIdentity struct {
	group         string
	kind          string
	namespace     string
	rawNamespace  string
	name          string
	resourceName  string
	scope         kubernetesResourceScope
	generatedName bool
}

func NewBundleOutputPlan(bundle Bundle) *BundleOutputPlan {
	scopes, err := newKubernetesResourceScopeIndexForBundle(bundle, DefaultKubernetesResourceCatalog())
	return outputpkg.NewBundleOutputPlan(bundle.Artifacts, scopes, err)
}

func NewBundleOutputPlanWithCatalog(bundle Bundle, catalog KubernetesResourceCatalog) (*BundleOutputPlan, error) {
	scopes, err := newKubernetesResourceScopeIndexForBundle(bundle, catalog)
	if err != nil {
		return nil, err
	}
	return outputpkg.NewBundleOutputPlan(bundle.Artifacts, scopes, nil), nil
}

func RenderPhaseArtifacts(bundle Bundle, phase string) ([]RenderedArtifact, error) {
	return NewBundleOutputPlan(bundle).RenderPhaseArtifacts(phase)
}

func RenderOperation(bundle Bundle, operation string) ([]RenderedFile, error) {
	if !validOperation(operation) {
		return nil, fmt.Errorf("unknown operation %q", operation)
	}
	return NewBundleOutputPlan(bundle).RenderOperation(operation)
}

func stableArtifactIDs(artifacts []Artifact) []string { return outputpkg.StableArtifactIDs(artifacts) }

func validLifecycleEvent(event string) bool { return outputpkg.ValidLifecycleEvent(event) }
func validOperation(operation string) bool  { return outputpkg.ValidOperation(operation) }
func validateLifecycleMetadata(artifacts []Artifact) error {
	return outputpkg.ValidateLifecycleMetadata(artifacts)
}

func lifecycleAllowsGeneratedName(lifecycle Lifecycle) bool {
	return kubernetespkg.AllowsGeneratedName(lifecycle)
}

func rejectDuplicateKubernetesResources(artifacts []Artifact, scopes kubernetesResourceScopeIndex) error {
	return kubernetespkg.ValidateResourceArtifacts(artifacts, scopes)
}

func kubernetesResourceIdentityOf(value Value) (kubernetesResourceIdentity, bool) {
	identity, isResource, err := validatedKubernetesResourceIdentityOf(value)
	if err != nil {
		return kubernetesResourceIdentity{}, false
	}
	if isResource {
		identity = normalizeKubernetesResourceIdentity(identity)
	}
	return identity, isResource
}

func validatedKubernetesResourceIdentityOf(value Value) (kubernetesResourceIdentity, bool, error) {
	return validatedKubernetesResourceIdentityOfWithGeneratedName(value, false)
}

func validatedKubernetesResourceIdentityOfWithGeneratedName(value Value, allowGeneratedName bool) (kubernetesResourceIdentity, bool, error) {
	return parseKubernetesResourceIdentity(value, allowGeneratedName)
}
