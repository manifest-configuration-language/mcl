package engine

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// KubernetesResourceCatalogRevisionは組み込みKubernetes Resource Catalogのrevisionを識別します。
const KubernetesResourceCatalogRevision = "mcl-kubernetes-resource-catalog-v1"

// KubernetesResourceScopeはKubernetes Resourceのnamespace scopeを表します。
type KubernetesResourceScope string

const (
	KubernetesResourceScopeNamespaced KubernetesResourceScope = "Namespaced"
	KubernetesResourceScopeCluster    KubernetesResourceScope = "Cluster"
)

// KubernetesResourceDescriptorはResourceのAPI identityとnamespace scopeを表します。
type KubernetesResourceDescriptor struct {
	APIGroup     string
	Kind         string
	ResourceName string
	Scope        KubernetesResourceScope
}

// KubernetesResourceCatalogはrevision付き組み込みCatalogに追加Descriptorを設定します。
type KubernetesResourceCatalog struct {
	Revision            string
	AdditionalResources []KubernetesResourceDescriptor
}

// DefaultKubernetesResourceCatalogは現在の組み込みCatalog revisionを返します。
func DefaultKubernetesResourceCatalog() KubernetesResourceCatalog {
	return KubernetesResourceCatalog{Revision: KubernetesResourceCatalogRevision}
}

// NewKubernetesResourceCatalogは現在の組み込みCatalogに追加Descriptorを設定して返します。
func NewKubernetesResourceCatalog(additionalResources ...KubernetesResourceDescriptor) KubernetesResourceCatalog {
	catalog := DefaultKubernetesResourceCatalog()
	catalog.AdditionalResources = slices.Clone(additionalResources)

	return catalog
}

// リソーステーブルで繰り返し現れるAPIグループ名。
const (
	groupAdmissionRegistration = "admissionregistration.k8s.io"
	groupApps                  = "apps"
	groupNetworking            = "networking.k8s.io"
	groupRBAC                  = "rbac.authorization.k8s.io"
	groupStorage               = "storage.k8s.io"
)

type kubernetesResourceScope uint8

const (
	kubernetesResourceUnknown kubernetesResourceScope = iota
	kubernetesResourceNamespaced
	kubernetesResourceCluster
)

type kubernetesResourceKey struct {
	group string
	kind  string
}

type kubernetesResourceDescriptor struct {
	resourceName string
	scope        kubernetesResourceScope
}

type kubernetesResourceScopeIndex map[kubernetesResourceKey]kubernetesResourceDescriptor

var builtinKubernetesResourceDescriptors = kubernetesResourceScopeIndex{
	{group: "", kind: "ConfigMap"}:                                                {resourceName: "configmaps", scope: kubernetesResourceNamespaced},
	{group: "", kind: "Endpoints"}:                                                {resourceName: "endpoints", scope: kubernetesResourceNamespaced},
	{group: "", kind: "Event"}:                                                    {resourceName: "events", scope: kubernetesResourceNamespaced},
	{group: "", kind: "LimitRange"}:                                               {resourceName: "limitranges", scope: kubernetesResourceNamespaced},
	{group: "", kind: "Namespace"}:                                                {resourceName: "namespaces", scope: kubernetesResourceCluster},
	{group: "", kind: "Node"}:                                                     {resourceName: "nodes", scope: kubernetesResourceCluster},
	{group: "", kind: "PersistentVolume"}:                                         {resourceName: "persistentvolumes", scope: kubernetesResourceCluster},
	{group: "", kind: "PersistentVolumeClaim"}:                                    {resourceName: "persistentvolumeclaims", scope: kubernetesResourceNamespaced},
	{group: "", kind: "Pod"}:                                                      {resourceName: "pods", scope: kubernetesResourceNamespaced},
	{group: "", kind: "PodTemplate"}:                                              {resourceName: "podtemplates", scope: kubernetesResourceNamespaced},
	{group: "", kind: "ReplicationController"}:                                    {resourceName: "replicationcontrollers", scope: kubernetesResourceNamespaced},
	{group: "", kind: "ResourceQuota"}:                                            {resourceName: "resourcequotas", scope: kubernetesResourceNamespaced},
	{group: "", kind: "Secret"}:                                                   {resourceName: "secrets", scope: kubernetesResourceNamespaced},
	{group: "", kind: "Service"}:                                                  {resourceName: "services", scope: kubernetesResourceNamespaced},
	{group: "", kind: "ServiceAccount"}:                                           {resourceName: "serviceaccounts", scope: kubernetesResourceNamespaced},
	{group: groupAdmissionRegistration, kind: "MutatingWebhookConfiguration"}:     {resourceName: "mutatingwebhookconfigurations", scope: kubernetesResourceCluster},
	{group: groupAdmissionRegistration, kind: "ValidatingAdmissionPolicy"}:        {resourceName: "validatingadmissionpolicies", scope: kubernetesResourceCluster},
	{group: groupAdmissionRegistration, kind: "ValidatingAdmissionPolicyBinding"}: {resourceName: "validatingadmissionpolicybindings", scope: kubernetesResourceCluster},
	{group: groupAdmissionRegistration, kind: "ValidatingWebhookConfiguration"}:   {resourceName: "validatingwebhookconfigurations", scope: kubernetesResourceCluster},
	{group: "apiregistration.k8s.io", kind: "APIService"}:                         {resourceName: "apiservices", scope: kubernetesResourceCluster},
	{group: "apiextensions.k8s.io", kind: kindCustomResourceDefinition}:           {resourceName: "customresourcedefinitions", scope: kubernetesResourceCluster},
	{group: groupApps, kind: "ControllerRevision"}:                                {resourceName: "controllerrevisions", scope: kubernetesResourceNamespaced},
	{group: groupApps, kind: "DaemonSet"}:                                         {resourceName: "daemonsets", scope: kubernetesResourceNamespaced},
	{group: groupApps, kind: "Deployment"}:                                        {resourceName: "deployments", scope: kubernetesResourceNamespaced},
	{group: groupApps, kind: "ReplicaSet"}:                                        {resourceName: "replicasets", scope: kubernetesResourceNamespaced},
	{group: groupApps, kind: "StatefulSet"}:                                       {resourceName: "statefulsets", scope: kubernetesResourceNamespaced},
	{group: "autoscaling", kind: "HorizontalPodAutoscaler"}:                       {resourceName: "horizontalpodautoscalers", scope: kubernetesResourceNamespaced},
	{group: "batch", kind: "CronJob"}:                                             {resourceName: "cronjobs", scope: kubernetesResourceNamespaced},
	{group: "batch", kind: "Job"}:                                                 {resourceName: "jobs", scope: kubernetesResourceNamespaced},
	{group: "certificates.k8s.io", kind: "CertificateSigningRequest"}:             {resourceName: "certificatesigningrequests", scope: kubernetesResourceCluster},
	{group: "coordination.k8s.io", kind: "Lease"}:                                 {resourceName: "leases", scope: kubernetesResourceNamespaced},
	{group: "discovery.k8s.io", kind: "EndpointSlice"}:                            {resourceName: "endpointslices", scope: kubernetesResourceNamespaced},
	{group: "flowcontrol.apiserver.k8s.io", kind: "FlowSchema"}:                   {resourceName: "flowschemas", scope: kubernetesResourceCluster},
	{group: "flowcontrol.apiserver.k8s.io", kind: "PriorityLevelConfiguration"}:   {resourceName: "prioritylevelconfigurations", scope: kubernetesResourceCluster},
	{group: groupNetworking, kind: "Ingress"}:                                     {resourceName: "ingresses", scope: kubernetesResourceNamespaced},
	{group: groupNetworking, kind: "IngressClass"}:                                {resourceName: "ingressclasses", scope: kubernetesResourceCluster},
	{group: groupNetworking, kind: "NetworkPolicy"}:                               {resourceName: "networkpolicies", scope: kubernetesResourceNamespaced},
	{group: "node.k8s.io", kind: "RuntimeClass"}:                                  {resourceName: "runtimeclasses", scope: kubernetesResourceCluster},
	{group: "policy", kind: "PodDisruptionBudget"}:                                {resourceName: "poddisruptionbudgets", scope: kubernetesResourceNamespaced},
	{group: groupRBAC, kind: "ClusterRole"}:                                       {resourceName: "clusterroles", scope: kubernetesResourceCluster},
	{group: groupRBAC, kind: "ClusterRoleBinding"}:                                {resourceName: "clusterrolebindings", scope: kubernetesResourceCluster},
	{group: groupRBAC, kind: "Role"}:                                              {resourceName: "roles", scope: kubernetesResourceNamespaced},
	{group: groupRBAC, kind: "RoleBinding"}:                                       {resourceName: "rolebindings", scope: kubernetesResourceNamespaced},
	{group: "scheduling.k8s.io", kind: "PriorityClass"}:                           {resourceName: "priorityclasses", scope: kubernetesResourceCluster},
	{group: groupStorage, kind: "CSIDriver"}:                                      {resourceName: "csidrivers", scope: kubernetesResourceCluster},
	{group: groupStorage, kind: "CSINode"}:                                        {resourceName: "csinodes", scope: kubernetesResourceCluster},
	{group: groupStorage, kind: "StorageClass"}:                                   {resourceName: "storageclasses", scope: kubernetesResourceCluster},
	{group: groupStorage, kind: "VolumeAttachment"}:                               {resourceName: "volumeattachments", scope: kubernetesResourceCluster},
}

func newKubernetesResourceScopeIndex(artifacts []Artifact) (kubernetesResourceScopeIndex, error) {
	return newKubernetesResourceScopeIndexWithCatalog(artifacts, DefaultKubernetesResourceCatalog())
}

func newKubernetesResourceScopeIndexWithCatalog(artifacts []Artifact, catalog KubernetesResourceCatalog) (kubernetesResourceScopeIndex, error) {
	if catalog.Revision != KubernetesResourceCatalogRevision {
		return nil, fmt.Errorf("unsupported Kubernetes Resource catalog revision %q", catalog.Revision)
	}
	index := maps.Clone(builtinKubernetesResourceDescriptors)
	for _, descriptor := range catalog.AdditionalResources {
		key, value, err := kubernetesResourceDescriptorFromPublic(descriptor)
		if err != nil {
			return nil, err
		}
		if _, exists := index[key]; exists {
			return nil, fmt.Errorf("additional Kubernetes Resource descriptor %q conflicts with an existing descriptor", kubernetesResourceKeyName(key))
		}
		index[key] = value
	}
	for _, artifact := range artifacts {
		value := nodeContents(artifact.Value)
		kind, exists := valueStringField(value, "kind")
		if !exists || kind != kindCustomResourceDefinition {
			continue
		}
		key, descriptor, err := kubernetesCustomResourceDescriptor(value)
		if err != nil {
			return nil, fmt.Errorf("CustomResourceDefinition scope: %w", err)
		}
		if existing, exists := index[key]; exists && existing != descriptor {
			return nil, fmt.Errorf("CustomResourceDefinition %q conflicts with an existing Kubernetes API resource", kubernetesResourceKeyName(key))
		}
		index[key] = descriptor
	}

	return index, nil
}

func KubernetesResourceScopeForBundle(bundle Bundle, catalog KubernetesResourceCatalog, apiVersion, kind string) (KubernetesResourceScope, bool, error) {
	index, err := newKubernetesResourceScopeIndexWithCatalog(bundle.Artifacts, catalog)
	if err != nil {
		return "", false, err
	}
	group, _, hasGroup := strings.Cut(apiVersion, "/")
	if !hasGroup {
		group = ""
	}
	descriptor, exists := index[kubernetesResourceKey{group: group, kind: kind}]
	if !exists {
		return "", false, nil
	}
	if descriptor.scope == kubernetesResourceCluster {
		return KubernetesResourceScopeCluster, true, nil
	}

	return KubernetesResourceScopeNamespaced, true, nil
}

func kubernetesResourceKeyName(key kubernetesResourceKey) string {
	if key.group == "" {
		return key.kind
	}

	return key.group + "/" + key.kind
}

func kubernetesResourceDescriptorFromPublic(descriptor KubernetesResourceDescriptor) (kubernetesResourceKey, kubernetesResourceDescriptor, error) {
	if descriptor.APIGroup != "" && len(validation.IsDNS1123Subdomain(descriptor.APIGroup)) != 0 {
		return kubernetesResourceKey{}, kubernetesResourceDescriptor{}, fmt.Errorf("invalid Kubernetes Resource API group %q", descriptor.APIGroup)
	}
	if len(validation.IsDNS1035Label(strings.ToLower(descriptor.Kind))) != 0 {
		return kubernetesResourceKey{}, kubernetesResourceDescriptor{}, fmt.Errorf("invalid Kubernetes Resource kind %q", descriptor.Kind)
	}
	if len(validation.IsDNS1123Label(descriptor.ResourceName)) != 0 {
		return kubernetesResourceKey{}, kubernetesResourceDescriptor{}, fmt.Errorf("invalid Kubernetes Resource name %q", descriptor.ResourceName)
	}
	var scope kubernetesResourceScope
	switch descriptor.Scope {
	case KubernetesResourceScopeNamespaced:
		scope = kubernetesResourceNamespaced
	case KubernetesResourceScopeCluster:
		scope = kubernetesResourceCluster
	default:
		return kubernetesResourceKey{}, kubernetesResourceDescriptor{}, fmt.Errorf("invalid Kubernetes Resource scope %q", descriptor.Scope)
	}

	return kubernetesResourceKey{group: descriptor.APIGroup, kind: descriptor.Kind}, kubernetesResourceDescriptor{resourceName: descriptor.ResourceName, scope: scope}, nil
}

func kubernetesCustomResourceDescriptor(value Value) (kubernetesResourceKey, kubernetesResourceDescriptor, error) {
	apiVersion, hasAPIVersion := valueStringField(value, "apiVersion")
	if !hasAPIVersion || apiVersion != "apiextensions.k8s.io/v1" {
		return kubernetesResourceKey{}, kubernetesResourceDescriptor{}, errors.New("apiVersion must be apiextensions.k8s.io/v1")
	}
	spec, ok := mapValue(value, "spec")
	if !ok {
		return kubernetesResourceKey{}, kubernetesResourceDescriptor{}, errors.New("spec is missing")
	}
	group, hasGroup := valueStringField(spec, "group")
	scopeText, hasScope := valueStringField(spec, "scope")
	names, hasNames := mapValue(nodeContents(spec), "names")
	plural, hasPlural := valueStringField(names, "plural")
	kind, hasKind := valueStringField(names, "kind")
	if !hasGroup || group == "" || !hasNames || !hasKind || kind == "" || !hasScope || !hasPlural || plural == "" {
		return kubernetesResourceKey{}, kubernetesResourceDescriptor{}, errors.New("spec.group, spec.scope, spec.names.kind, and non-empty spec.names.plural are required")
	}
	var scope kubernetesResourceScope
	switch scopeText {
	case "Namespaced":
		scope = kubernetesResourceNamespaced
	case "Cluster":
		scope = kubernetesResourceCluster
	default:
		return kubernetesResourceKey{}, kubernetesResourceDescriptor{}, fmt.Errorf("spec.scope must be Namespaced or Cluster, got %q", scopeText)
	}

	publicScope := KubernetesResourceScopeNamespaced
	if scope == kubernetesResourceCluster {
		publicScope = KubernetesResourceScopeCluster
	}

	return kubernetesResourceDescriptorFromPublic(KubernetesResourceDescriptor{
		APIGroup: group, Kind: kind, ResourceName: plural, Scope: publicScope,
	})
}

func validKubernetesAPIVersion(version string) bool {
	if !strings.HasPrefix(version, "v") {
		return false
	}
	version = strings.TrimPrefix(version, "v")
	majorEnd := 0
	for majorEnd < len(version) && version[majorEnd] >= '0' && version[majorEnd] <= '9' {
		majorEnd++
	}
	if majorEnd == 0 {
		return false
	}
	if majorEnd == len(version) {
		return true
	}
	for _, suffix := range []string{"alpha", "beta"} {
		if !strings.HasPrefix(version[majorEnd:], suffix) {
			continue
		}
		minor := strings.TrimPrefix(version[majorEnd:], suffix)
		if minor == "" {
			return false
		}
		for _, digit := range minor {
			if digit < '0' || digit > '9' {
				return false
			}
		}

		return true
	}

	return false
}
