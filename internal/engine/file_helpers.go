package engine

import (
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/manifest-configuration-language/mcl/internal/project"
)

const maxYAMLBytes = 16 << 20

const (
	maxYAMLNodes = 300_000
	maxYAMLDepth = 128
)

const (
	kustomizeSourceLocationLabel            = "Original Kustomize resource"
	kustomizeTransformedSourceLocationLabel = "Kustomize input transformed into this value"
	helmRenderedTemplateSourceLabel         = "Rendered from Helm template"
)

func pathWithin(root, target string) bool {
	relative, err := filepath.Rel(root, target)

	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// projectFilesystem preserves the engine's existing access points while project.Filesystem owns path handling.
type projectFilesystem struct {
	projectFS *project.Filesystem
	root      string
	files     fs.FS
	osBacked  bool
}

func newOSProjectFilesystem(root string) *projectFilesystem {
	return wrapProjectFilesystem(project.NewOSFilesystem(root))
}

func newProjectFilesystem(root string, files fs.FS) *projectFilesystem {
	return wrapProjectFilesystem(project.NewFilesystem(root, files))
}

func wrapProjectFilesystem(filesystem *project.Filesystem) *projectFilesystem {
	return &projectFilesystem{
		projectFS: filesystem,
		root:      filesystem.Root(),
		files:     filesystem.FS(),
		osBacked:  filesystem.OSBacked(),
	}
}

func (filesystem *projectFilesystem) readPaths() []string {
	return filesystem.projectFS.ReadPaths()
}

func (filesystem *projectFilesystem) recordRead(name string) {
	filesystem.projectFS.RecordRead(name)
}

func projectFilesystemFor(root string, filesystems []*projectFilesystem) *projectFilesystem {
	if len(filesystems) > 0 && filesystems[0] != nil {
		return filesystems[0]
	}

	return newOSProjectFilesystem(root)
}

func (filesystem *projectFilesystem) relativePath(filename string) (string, error) {
	return filesystem.projectFS.RelativePath(filename)
}

func (filesystem *projectFilesystem) resolveReadPath(filename string) (string, error) {
	return filesystem.projectFS.ResolveReadPath(filename)
}

func (filesystem *projectFilesystem) resolveModuleImport(importer, modulePath string) (string, error) {
	return filesystem.projectFS.ResolveModuleImport(importer, modulePath)
}

func (filesystem *projectFilesystem) readLimitedFile(filename string) ([]byte, error) {
	return filesystem.projectFS.ReadLimitedFile(filename, maxMCLSourceBytes)
}
