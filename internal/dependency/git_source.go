package dependency

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/capability"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/sideband"
	"github.com/go-git/go-git/v5/plumbing/transport"
	gitHTTP "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/manifest-configuration-language/mcl/internal/netpolicy"
)

const (
	maxKustomizeGitFiles     = 10000
	maxGitFetchDuration      = 90 * time.Second
	maxGitFetchResponseBytes = int64(128 << 20)
	maxGitObjectStoreBytes   = int64(256 << 20)
)

func ValidateGitSource(source string) error {
	if !strings.HasPrefix(source, "git+") {
		return errors.New("kustomize Git source must use git+https")
	}
	parsed, err := url.Parse(strings.TrimPrefix(source, "git+"))
	if err != nil || parsed.Scheme != schemeHTTPS || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" || parsed.Path == "" || strings.Contains(parsed.Path, "\\") {
		return errors.New("kustomize Git source must be an absolute HTTPS repository URL without user information, query, or fragment")
	}

	return nil
}

func ValidGitRevision(revision string) bool {
	if len(revision) != 40 || strings.ToLower(revision) != revision {
		return false
	}
	for _, character := range revision {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}

	return true
}

func ValidateGitDirectory(directory string) error {
	if directory == "" || strings.Contains(directory, "\\") || path.IsAbs(directory) || path.Clean(directory) != directory || directory == ".." || strings.HasPrefix(directory, "../") {
		return errors.New("kustomize Git directory must be a normalized relative path")
	}

	return nil
}

func GitSourceKey(source, revision, directory string) string {
	return source + "\n" + revision + "\n" + directory
}

func fetchGitTree(ctx context.Context, source, revision, directory string) ([]byte, error) {
	if err := ValidateGitSource(source); err != nil {
		return nil, err
	}
	if !ValidGitRevision(revision) {
		return nil, errors.New("kustomize Git revision must be a complete 40-character lowercase commit SHA-1")
	}
	if err := ValidateGitDirectory(directory); err != nil {
		return nil, err
	}
	parsed, _ := url.Parse(strings.TrimPrefix(source, "git+"))

	fetchContext, cancelFetch := context.WithTimeout(ctx, maxGitFetchDuration)
	defer cancelFetch()
	if err := fetchContext.Err(); err != nil {
		return nil, fmt.Errorf("prepare HTTPS Git transport: %w", err)
	}
	transport, err := netpolicy.NewExternalHTTPTransport()
	if err != nil {
		return nil, err
	}
	strictHTTP := &http.Client{
		Transport: &gitFetchLimitTransport{base: transport, limit: maxGitFetchResponseBytes},
		Timeout:   maxGitFetchDuration,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("HTTPS Git redirects are not supported")
		},
	}
	if err := fetchContext.Err(); err != nil {
		return nil, fmt.Errorf("prepare HTTPS Git transport: %w", err)
	}

	repositoryPath, err := os.MkdirTemp("", "mcl-git-objects-*")
	if err != nil {
		return nil, fmt.Errorf("create temporary Git storage: %w", err)
	}
	defer os.RemoveAll(repositoryPath)
	objectStoreFS := newQuotaFilesystem(osfs.New(repositoryPath), maxGitObjectStoreBytes)
	repositoryStorage := filesystem.NewStorage(objectStoreFS, cache.NewObjectLRUDefault())
	repository, err := git.Init(repositoryStorage, nil)
	if err != nil {
		return nil, fmt.Errorf("initialize temporary Git storage: %w", err)
	}
	if err := fetchGitCommit(fetchContext, parsed.String(), revision, strictHTTP, repository); err != nil {
		return nil, fmt.Errorf("fetch Kustomize Git source %q at %s: %w", source, revision, err)
	}
	commitHash := plumbing.NewHash(revision)
	commit, err := repository.CommitObject(commitHash)
	if err != nil {
		return nil, fmt.Errorf("kustomize Git revision %s is not a commit: %w", revision, err)
	}
	if commit.Hash.String() != revision {
		return nil, errors.New("kustomize Git source did not resolve to the requested commit")
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("read Kustomize Git commit tree: %w", err)
	}

	return archiveGitDirectory(tree, directory)
}

func fetchGitCommit(ctx context.Context, source, revision string, httpClient *http.Client, repository *git.Repository) (retErr error) {
	endpoint, err := transport.NewEndpoint(source)
	if err != nil {
		return fmt.Errorf("parse Kustomize Git endpoint: %w", err)
	}
	session, err := gitHTTP.NewClient(httpClient).NewUploadPackSession(endpoint, nil)
	if err != nil {
		return fmt.Errorf("start Kustomize Git upload-pack session: %w", err)
	}
	defer func() {
		retErr = errors.Join(retErr, session.Close())
	}()

	advertisement, err := session.AdvertisedReferencesContext(ctx)
	if err != nil {
		return fmt.Errorf("read Kustomize Git references: %w", err)
	}
	if !advertisement.Capabilities.Supports(capability.AllowTipSHA1InWant) && !advertisement.Capabilities.Supports(capability.AllowReachableSHA1InWant) {
		return git.ErrExactSHA1NotSupported
	}
	if !advertisement.Capabilities.Supports(capability.Shallow) {
		return errors.New("kustomize Git server does not support shallow fetches")
	}
	request := packp.NewUploadPackRequestFromCapabilities(advertisement.Capabilities)
	if err := request.Capabilities.Set(capability.Shallow); err != nil {
		return fmt.Errorf("configure Kustomize Git shallow fetch: %w", err)
	}
	if advertisement.Capabilities.Supports(capability.NoProgress) {
		if err := request.Capabilities.Set(capability.NoProgress); err != nil {
			return fmt.Errorf("disable Kustomize Git fetch progress: %w", err)
		}
	}
	request.Depth = packp.DepthCommits(1)
	request.Wants = []plumbing.Hash{plumbing.NewHash(revision)}
	response, err := session.UploadPack(ctx, request)
	if err != nil {
		return fmt.Errorf("request Kustomize Git commit: %w", err)
	}
	defer func() {
		retErr = errors.Join(retErr, response.Close())
	}()

	if len(response.Shallows) > 0 {
		shallowCommits, err := repository.Storer.Shallow()
		if err != nil {
			return fmt.Errorf("read Kustomize Git shallow commits: %w", err)
		}
		shallowCommits = append(shallowCommits, response.Shallows...)
		if err := repository.Storer.SetShallow(shallowCommits); err != nil {
			return fmt.Errorf("store Kustomize Git shallow commits: %w", err)
		}
	}

	var packReader io.Reader = response
	switch {
	case request.Capabilities.Supports(capability.Sideband64k):
		packReader = sideband.NewDemuxer(sideband.Sideband64k, response)
	case request.Capabilities.Supports(capability.Sideband):
		packReader = sideband.NewDemuxer(sideband.Sideband, response)
	}
	if err := packfile.UpdateObjectStorage(repository.Storer, packReader); err != nil {
		return fmt.Errorf("store Kustomize Git packfile: %w", err)
	}

	return nil
}

func archiveGitDirectory(tree *object.Tree, directory string) ([]byte, error) {
	if directory != "." {
		entry, err := tree.FindEntry(directory)
		if err != nil || entry.Mode != filemode.Dir {
			return nil, fmt.Errorf("kustomize Git directory %q does not exist", directory)
		}
		tree, err = tree.Tree(directory)
		if err != nil {
			return nil, fmt.Errorf("read Kustomize Git directory %q: %w", directory, err)
		}
	}
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	files := tree.Files()
	defer files.Close()
	var totalBytes int64
	var fileCount int
	err := files.ForEach(func(file *object.File) error {
		if file.Mode != filemode.Regular && file.Mode != filemode.Executable {
			return fmt.Errorf("kustomize Git source contains unsupported file mode for %q", file.Name)
		}
		fileCount++
		if fileCount > maxKustomizeGitFiles {
			return fmt.Errorf("kustomize Git source contains more than %d files", maxKustomizeGitFiles)
		}
		name := path.Clean(file.Name)
		if name == "." || path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("kustomize Git source contains invalid path %q", file.Name)
		}
		header := &tar.Header{Name: name, Mode: 0o644, Size: file.Size, Typeflag: tar.TypeReg}
		if file.Mode == filemode.Executable {
			header.Mode = 0o755
		}
		totalBytes += file.Size
		if totalBytes > maxKustomizeSourceBytes {
			return fmt.Errorf("kustomize Git source exceeds the %d-byte limit", maxKustomizeSourceBytes)
		}
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		reader, err := file.Reader()
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(writer, io.LimitReader(reader, file.Size))
		closeErr := reader.Close()
		if copyErr != nil {
			return copyErr
		}

		return closeErr
	})
	if err != nil {
		return nil, fmt.Errorf("archive Kustomize Git directory: %w", err)
	}
	if fileCount == 0 {
		return nil, errors.New("kustomize Git directory contains no files")
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("finish Kustomize Git archive: %w", err)
	}
	if archive.Len() > maxRemoteGitTreeSize {
		return nil, fmt.Errorf("kustomize Git archive exceeds the %d-byte limit", maxRemoteGitTreeSize)
	}

	return archive.Bytes(), nil
}
