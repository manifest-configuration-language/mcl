package engine

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
)

const (
	targetFilename     = "target.mcl"
	importerFilename   = "importer.mcl"
	importerSourceText = "import \"./target.mcl\" as target\nemit target.value\n"
)

func TestEvaluationOptionsSelectBuiltinKubernetesSchemaVersion(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "project")
	path := filepath.Join(root, projectFileEntrypoint)
	const source = `let workload = node:
  apiVersion: "scheduling.k8s.io/v1alpha2"
  kind: "Workload"
  metadata:
    name: "sample"
  spec:
    podGroupTemplates:
      - name: "basic"
        schedulingPolicy:
          basic: {}
emit workload
`
	files := newProjectFilesystem(root, fstest.MapFS{
		projectConfigFileName: &fstest.MapFile{Data: []byte("")},
		projectFileEntrypoint: &fstest.MapFile{Data: []byte(source)},
	})
	if _, _, err := evaluateSourceWithFilesystemAndParameters(t.Context(), path, source, nil, files, nil, EvaluationOptions{KubernetesVersion: "v1.36.4"}); err != nil {
		t.Fatalf("evaluate with Kubernetes v1.36.4: %v", err)
	}
	if _, _, err := evaluateSourceWithFilesystemAndParameters(t.Context(), path, source, nil, files, nil, EvaluationOptions{KubernetesVersion: "v1.37.1"}); err == nil || !strings.Contains(err.Error(), "no schema is registered") {
		t.Fatalf("evaluate with Kubernetes v1.37.1 error = %v, want unavailable Workload schema", err)
	}
}

func TestEvaluateFileReturnsOpenedProjectPaths(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	files := map[string]string{
		projectConfigFileName: "",
		"main.mcl": `import "./child.mcl" as child
let resource =
  apiVersion: "v1"
  kind: "ConfigMap"
  metadata:
    name: child.name
emit resource
`,
		testChildModuleFilename: `export let name = "tracked"
`,
	}
	for name, source := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	bundle, paths, err := EvaluateFileForOperationWithParametersAndOptionsAndReadFilesContext(t.Context(), filepath.Join(root, "main.mcl"), "create", nil, EvaluationOptions{})
	if err != nil {
		t.Fatalf("evaluate with read tracking: %v", err)
	}
	if len(bundle.Artifacts) != 1 {
		t.Fatalf("bundle artifact count = %d, want 1", len(bundle.Artifacts))
	}
	for _, want := range []string{"child.mcl", "main.mcl", projectConfigFileName} {
		if !slices.Contains(paths, want) {
			t.Errorf("read paths %v do not contain %q", paths, want)
		}
	}
}

func TestTypedResourceMapAcceptsIntOrStringNetworkPolicyPort(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "project")
	path := filepath.Join(root, projectFileEntrypoint)
	const source = `export fn resources(namespace: String) -> Map<String, Any> =
  networkPolicy: node:
    apiVersion: "networking.k8s.io/v1"
    kind: "NetworkPolicy"
    metadata:
      name: "example"
      namespace: namespace
    spec:
      egress:
        -
          ports:
            -
              port: 53
              protocol: "UDP"
          to:
            -
              ipBlock:
                cidr: "0.0.0.0/0"
      podSelector: {}
      policyTypes:
        - "Egress"

let manifests = resources("example")
emit manifests.networkPolicy
`
	files := newProjectFilesystem(root, fstest.MapFS{
		projectConfigFileName: &fstest.MapFile{Data: []byte("")},
		projectFileEntrypoint: &fstest.MapFile{Data: []byte(source)},
	})
	if _, _, err := evaluateSourceWithFilesystemAndParameters(t.Context(), path, source, nil, files, nil, EvaluationOptions{KubernetesVersion: "v1.36.4"}); err != nil {
		t.Fatalf("evaluate typed resource map with integer NetworkPolicy port: %v", err)
	}
}

func TestTypedResourceMapRetainsRuntimeValidationForAnyFields(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "project")
	path := filepath.Join(root, projectFileEntrypoint)
	const source = `fn service(labels: Map<String, String>, ports: List<Map<String, Any>>) -> Map<String, Any> = node:
  apiVersion: "v1"
  kind: "Service"
  metadata:
    name: "example"
    namespace: "default"
    labels: labels
  spec:
    selector:
      app: "example"
    ports: ports

let ports: List<Map<String, Any>> =
  - port: 80
    targetPort: "http"
    protocol: "TCP"

let labels: Map<String, String> =
  app: "example"

emit service(labels, ports)
`
	files := newProjectFilesystem(root, fstest.MapFS{
		projectConfigFileName: &fstest.MapFile{Data: []byte("")},
		projectFileEntrypoint: &fstest.MapFile{Data: []byte(source)},
	})
	if _, _, err := evaluateSourceWithFilesystemAndParameters(t.Context(), path, source, nil, files, nil, EvaluationOptions{KubernetesVersion: "v1.36.4"}); err != nil {
		t.Fatalf("evaluate resource with runtime-validated Any fields: %v", err)
	}
}

func TestDependentMCLSourcesForProjectFS(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "virtual-project")
	files := fstest.MapFS{
		targetFilename:             &fstest.MapFile{Data: []byte("export let value = 1\n")},
		"modules/middle.mcl":       &fstest.MapFile{Data: []byte("import \"../target.mcl\" as target\nexport let value = target.value\n")},
		"modules/main.mcl":         &fstest.MapFile{Data: []byte("import \"./middle.mcl\" as middle\nemit middle.value\n")},
		"modules/unrelated.mcl":    &fstest.MapFile{Data: []byte("emit 1\n")},
		".mcl/cache/cached.mcl":    &fstest.MapFile{Data: []byte("import \"../../target.mcl\" as target\n")},
		"node_modules/ignored.mcl": &fstest.MapFile{Data: []byte("import \"../target.mcl\" as target\n")},
	}

	sources, err := DependentMCLSourcesForProjectFS(root, targetFilename, files)
	if err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{targetFilename, "modules/middle.mcl", "modules/main.mcl"} {
		if _, exists := sources[filepath.Join(root, filepath.FromSlash(relative))]; !exists {
			t.Errorf("dependent source result omitted %q", relative)
		}
	}
	for _, relative := range []string{"modules/unrelated.mcl", ".mcl/cache/cached.mcl", "node_modules/ignored.mcl"} {
		if _, exists := sources[filepath.Join(root, filepath.FromSlash(relative))]; exists {
			t.Errorf("dependent source result included %q", relative)
		}
	}
}

func TestDependentMCLSourceIndexSupportsProjectFSUpdatesAndOverlays(t *testing.T) {
	t.Parallel()
	const root = "/virtual-project"
	files := fstest.MapFS{
		targetFilename:   &fstest.MapFile{Data: []byte("export let value = 1\n")},
		importerFilename: &fstest.MapFile{Data: []byte("let local = 1\n")},
	}
	importer := filepath.Join(root, importerFilename)
	index := &DependentMCLSourceIndex{}
	if _, err := index.SourcesForProjectFS(root, targetFilename, files, nil); err != nil {
		t.Fatal(err)
	}

	importSource := importerSourceText
	files[importerFilename].Data = []byte(importSource)
	if err := index.ApplyFileChanges([]DependentMCLFileChange{{Path: importer}}); err != nil {
		t.Fatal(err)
	}
	dependent, err := index.SourcesFromProjectFSIndex(root, targetFilename, files, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := dependent[importer]; got != importSource {
		t.Fatalf("incremental filesystem source = %q, want %q", got, importSource)
	}

	overlay := "let local = 2\n"
	dependent, err = index.SourcesFromProjectFSIndex(root, targetFilename, files, map[string]string{importer: overlay})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := dependent[importer]; exists {
		t.Fatal("overlay removed the importer edge, but importer remained in dependent sources")
	}

	updatedImportSource := "import \"./target.mcl\" as target\nlet value = target.value\nemit value\n"
	files[importerFilename].Data = []byte(updatedImportSource)
	if err := index.ApplyFileChanges([]DependentMCLFileChange{{Path: importer}}); err != nil {
		t.Fatal(err)
	}
	dependent, err = index.SourcesFromProjectFSIndex(root, targetFilename, files, map[string]string{importer: overlay})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := dependent[importer]; exists {
		t.Fatal("filesystem update replaced an open document overlay")
	}
	dependent, err = index.SourcesFromProjectFSIndex(root, targetFilename, files, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := dependent[importer]; got != updatedImportSource {
		t.Fatalf("closing overlay restored %q, want %q", got, updatedImportSource)
	}

	delete(files, importerFilename)
	if err := index.ApplyFileChanges([]DependentMCLFileChange{{Path: importer, Deleted: true}}); err != nil {
		t.Fatal(err)
	}
	dependent, err = index.SourcesFromProjectFSIndex(root, targetFilename, files, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := dependent[importer]; exists {
		t.Fatal("deleted importer remained in dependent sources")
	}
}

func TestDependentMCLSourceIndexReusesUnchangedWorkspaceSnapshot(t *testing.T) {
	t.Parallel()
	const root = "/virtual-project"
	files := fstest.MapFS{
		targetFilename:   &fstest.MapFile{Data: []byte("export let value = 1\n")},
		importerFilename: &fstest.MapFile{Data: []byte(importerSourceText)},
	}
	index := &DependentMCLSourceIndex{}
	if snapshot := index.WorkspaceSnapshot(); snapshot != nil {
		t.Fatal("workspace snapshot is non-nil before initialization")
	}
	if _, err := index.SourcesForProjectFS(root, targetFilename, files, nil); err != nil {
		t.Fatal(err)
	}
	first := index.WorkspaceSnapshot()
	if first == nil {
		t.Fatal("workspace snapshot is nil after initialization")
	}
	if _, err := index.SourcesForProjectFS(root, targetFilename, files, nil); err != nil {
		t.Fatal(err)
	}
	current := index.WorkspaceSnapshot()
	if current == nil || current != first {
		t.Fatalf("unchanged refresh snapshot = %p, want same pointer %p", current, first)
	}
	if current.Generation() != first.Generation() {
		t.Fatalf("unchanged refresh generation = %d, want %d", current.Generation(), first.Generation())
	}

	otherFilename := "other.mcl"
	files[otherFilename] = &fstest.MapFile{Data: []byte("export let value = 2\n")}
	files[importerFilename].Data = []byte("import \"./other.mcl\" as other\nemit other.value\n")
	if _, err := index.SourcesForProjectFS(root, targetFilename, files, nil); err != nil {
		t.Fatal(err)
	}
	current = index.WorkspaceSnapshot()
	if current == nil || current == first || current.Generation() != first.Generation()+1 {
		var generation uint64
		if current != nil {
			generation = current.Generation()
		}
		t.Fatalf("changed refresh snapshot = %p generation %d, want new pointer at generation %d", current, generation, first.Generation()+1)
	}
	if got, want := current.Dependencies(filepath.Join(root, importerFilename)), []string{filepath.Join(root, otherFilename)}; !slices.Equal(got, want) {
		t.Fatalf("importer dependencies = %v, want %v", got, want)
	}

	index.Invalidate()
	if snapshot := index.WorkspaceSnapshot(); snapshot != nil {
		t.Fatal("workspace snapshot is non-nil after invalidation")
	}
}

func TestDependentMCLSourceIndexReturnsSnapshotWithSources(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for path, source := range map[string]string{
		projectConfigFileName: "",
		targetFilename:        "export let value = 1\n",
		importerFilename:      importerSourceText,
	} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	index := &DependentMCLSourceIndex{}
	target := filepath.Join(root, targetFilename)
	importer, err := filepath.EvalSymlinks(filepath.Join(root, importerFilename))
	if err != nil {
		t.Fatal(err)
	}
	sources, snapshot, err := index.SourcesWithSnapshotWithContext(t.Context(), target, []string{root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := sources[importer]; got != importerSourceText {
		t.Fatalf("dependent importer source = %q, want %q", got, importerSourceText)
	}
	if snapshot == nil || snapshot != index.WorkspaceSnapshot() {
		t.Fatalf("returned workspace snapshot = %p, current snapshot = %p", snapshot, index.WorkspaceSnapshot())
	}
}

func TestDependentMCLSourceIndexAppliesWatchedChangesAndOverlays(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for path, source := range map[string]string{
		projectConfigFileName: "",
		targetFilename:        "export let value = \"old\"\n",
		importerFilename:      "let local = \"old\"\n",
	} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(root, targetFilename)
	importer := filepath.Join(root, importerFilename)
	importer, err := filepath.EvalSymlinks(importer)
	if err != nil {
		t.Fatal(err)
	}
	index := &DependentMCLSourceIndex{}
	if _, err := index.Sources(target, []string{root}, nil); err != nil {
		t.Fatal(err)
	}

	importSource := importerSourceText
	if err := os.WriteFile(importer, []byte(importSource), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := index.ApplyFileChanges([]DependentMCLFileChange{{Path: importer}}); err != nil {
		t.Fatal(err)
	}
	dependent, err := index.SourcesFromIndex(target, []string{root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := dependent[importer]; got != importSource {
		t.Fatalf("incremental index source = %q, want %q", got, importSource)
	}
	snapshot := index.WorkspaceSnapshot()
	_, got, exists := snapshot.Source(importer)
	if want := sha256.Sum256([]byte(importSource)); !exists || got != want {
		t.Fatalf("source hash = %x, want hash of current source %x", got, want)
	}

	overlay := "let local = \"overlay\"\n"
	dependent, err = index.SourcesFromIndex(target, []string{root}, map[string]string{importer: overlay})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := dependent[importer]; exists {
		t.Fatal("overlay removed the importer edge, but importer remained in dependent sources")
	}
	diskSource := "import \"./target.mcl\" as target\nlet changed = \"disk\"\nemit target.value\n"
	if err := os.WriteFile(importer, []byte(diskSource), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := index.ApplyFileChanges([]DependentMCLFileChange{{Path: importer}}); err != nil {
		t.Fatal(err)
	}
	dependent, err = index.SourcesFromIndex(target, []string{root}, map[string]string{importer: overlay})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := dependent[importer]; exists {
		t.Fatal("disk watcher change replaced an open document overlay")
	}
	dependent, err = index.SourcesFromIndex(target, []string{root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := dependent[importer]; got != diskSource {
		t.Fatalf("closed overlay restored %q, want disk source %q", got, diskSource)
	}
	snapshot = index.WorkspaceSnapshot()
	_, got, exists = snapshot.Source(importer)
	if want := sha256.Sum256([]byte(diskSource)); !exists || got != want {
		t.Fatalf("closed overlay source hash = %x, want hash of disk source %x", got, want)
	}
}

func TestDependentMCLSourceIndexInvalidatesOnDeletedImport(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for path, source := range map[string]string{
		projectConfigFileName: "",
		targetFilename:        "export let value = \"old\"\n",
		importerFilename:      importerSourceText,
	} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(root, targetFilename)
	index := &DependentMCLSourceIndex{}
	if _, err := index.Sources(target, []string{root}, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := index.ApplyFileChanges([]DependentMCLFileChange{{Path: target, Deleted: true}}); err == nil {
		t.Fatal("deleting an imported module left a valid reverse index")
	}
	if index.WorkspaceSnapshot() != nil {
		t.Fatal("failed watcher update kept the index marked complete")
	}
}

func TestDependentMCLSourceIndexIgnoresWatchedFilesInCache(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for path, source := range map[string]string{
		projectConfigFileName: "",
		targetFilename:        "export let value = \"old\"\n",
	} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cache := filepath.Join(root, ".mcl", "cache")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, targetFilename)
	cachedImporter := filepath.Join(cache, importerFilename)
	index := &DependentMCLSourceIndex{}
	if _, err := index.Sources(target, []string{root}, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachedImporter, []byte("import \"../../target.mcl\" as target\nemit target.value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := index.ApplyFileChanges([]DependentMCLFileChange{{Path: cachedImporter}}); err != nil {
		t.Fatal(err)
	}
	dependent, err := index.SourcesFromIndex(target, []string{root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := dependent[cachedImporter]; exists {
		t.Fatal("incremental index included a module from the ignored cache directory")
	}
}

func TestDependentMCLSourceIndexConcurrentUpdates(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for path, source := range map[string]string{
		projectConfigFileName: "",
		targetFilename:        "export let value = 1\n",
		importerFilename:      importerSourceText,
	} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(root, targetFilename)
	importer := filepath.Join(root, importerFilename)
	index := &DependentMCLSourceIndex{}
	if _, err := index.Sources(target, []string{root}, nil); err != nil {
		t.Fatal(err)
	}

	var workers sync.WaitGroup
	workers.Go(func() {
		for range 20 {
			if _, err := index.SourcesFromIndexWithContext(t.Context(), target, []string{root}, nil); err != nil {
				t.Errorf("read dependent sources: %v", err)
			}
		}
	})
	workers.Go(func() {
		for range 20 {
			index.Invalidate()
			if _, err := index.SourcesWithContext(t.Context(), target, []string{root}, nil); err != nil {
				t.Errorf("rebuild dependent sources: %v", err)
			}
		}
	})
	workers.Go(func() {
		for range 20 {
			if err := index.ApplyFileChanges([]DependentMCLFileChange{{Path: importer}}); err != nil {
				t.Errorf("apply watched-file change: %v", err)
			}
		}
	})
	workers.Wait()
}
