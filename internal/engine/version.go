package engine

import (
	"runtime"
	"runtime/debug"

	"github.com/manifest-configuration-language/mcl/internal/dependency"
)

var ToolchainVersion = "0.1.0-dev"

var toolchainVersionProvider = func() string { return ToolchainVersion }

func SetToolchainVersionProvider(provider func() string) {
	if provider != nil {
		toolchainVersionProvider = provider
	}
}

type BuildIdentity struct {
	Revision string
	Modified string
}

func CurrentBuildIdentity() BuildIdentity {
	identity := BuildIdentity{}
	buildInfo, ok := debug.ReadBuildInfo()
	if !ok {
		return identity
	}
	for _, setting := range buildInfo.Settings {
		switch setting.Key {
		case "vcs.revision":
			identity.Revision = setting.Value
		case "vcs.modified":
			identity.Modified = setting.Value
		}
	}

	return identity
}

func dependencyToolchainIdentity() dependency.ToolchainIdentity {
	identity := CurrentBuildIdentity()
	toolchain := dependency.ToolchainIdentity{MCL: toolchainVersionProvider(), Revision: identity.Revision, Modified: identity.Modified, Go: runtime.Version()}
	buildInfo, ok := debug.ReadBuildInfo()
	if !ok {
		return toolchain
	}
	for _, module := range buildInfo.Deps {
		version := module.Version
		if module.Replace != nil {
			version = module.Replace.Version
		}
		switch module.Path {
		case "helm.sh/helm/v4":
			toolchain.Helm = version
		case "sigs.k8s.io/kustomize/api":
			toolchain.KustomizeAPI = version
		case "sigs.k8s.io/kustomize/kyaml":
			toolchain.KustomizeKYAML = version
		}
	}

	return toolchain
}
