package languageservice

import (
	"io/fs"
	"path"
	"strings"
	"testing/fstest"
)

func projectSourceSnapshot(params NavigationParams, projectFiles fs.FS) (map[string]string, fstest.MapFS, bool) {
	if projectFiles == nil || !fs.ValidPath(params.Entrypoint) || params.Entrypoint == "." || strings.Contains(params.Entrypoint, `\`) || path.Ext(params.Entrypoint) != ".mcl" {
		return nil, nil, false
	}
	sources := make(map[string]string)
	files := make(fstest.MapFS)
	err := fs.WalkDir(projectFiles, ".", func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.Type().IsRegular() || path.Ext(filePath) != ".mcl" {
			return nil
		}
		content, err := fs.ReadFile(projectFiles, filePath)
		if err != nil {
			return err
		}
		sources[filePath] = string(content)
		files[filePath] = &fstest.MapFile{Data: content}

		return nil
	})
	if err != nil {
		return nil, nil, false
	}
	sources[params.Entrypoint] = params.Source
	files[params.Entrypoint] = &fstest.MapFile{Data: []byte(params.Source)}

	return sources, files, true
}
