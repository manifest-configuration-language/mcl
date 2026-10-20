package dependency

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/go-git/go-billy/v5"
)

var (
	errGitFetchResponseLimit = errors.New("git fetch response exceeds the byte limit")
	errGitObjectStoreLimit   = errors.New("git object store exceeds the disk limit")
)

type gitFetchLimitTransport struct {
	base  http.RoundTripper
	limit int64
	mu    sync.Mutex
	bytes int64
}

func (t *gitFetchLimitTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	if response.Body != nil {
		response.Body = &gitFetchLimitBody{ReadCloser: response.Body, transport: t}
	}

	return response, nil
}

type gitFetchLimitBody struct {
	io.ReadCloser
	transport *gitFetchLimitTransport
}

func (b *gitFetchLimitBody) Read(buffer []byte) (int, error) {
	t := b.transport
	t.mu.Lock()
	defer t.mu.Unlock()

	remaining := t.limit - t.bytes
	if remaining < 0 {
		return 0, errGitFetchResponseLimit
	}
	if int64(len(buffer)) > remaining+1 {
		buffer = buffer[:remaining+1]
	}
	n, err := b.ReadCloser.Read(buffer)
	t.bytes += int64(n)
	if int64(n) > remaining {
		return 0, errGitFetchResponseLimit
	}

	return n, err
}

type quotaFilesystem struct {
	billy.Filesystem
	quota  *filesystemQuota
	prefix string
}

type filesystemQuota struct {
	mu    sync.Mutex
	limit int64
	used  int64
	files map[string]*quotaFileState
}

type quotaFileState struct {
	size        int64
	openHandles int
	linked      bool
}

func newQuotaFilesystem(filesystem billy.Filesystem, limit int64) *quotaFilesystem {
	return &quotaFilesystem{
		Filesystem: filesystem,
		quota: &filesystemQuota{
			limit: limit,
			files: make(map[string]*quotaFileState),
		},
	}
}

func (f *quotaFilesystem) OpenFile(filename string, flag int, perm os.FileMode) (billy.File, error) {
	f.quota.mu.Lock()
	defer f.quota.mu.Unlock()

	key := f.key(filename)
	info, statErr := f.Stat(filename)
	file, err := f.Filesystem.OpenFile(filename, flag, perm)
	if err != nil {
		return nil, err
	}
	state, tracked := f.quota.files[key]
	if !tracked {
		state = &quotaFileState{linked: true}
		if statErr == nil {
			state.size = info.Size()
			if err := f.quota.reserve(state.size); err != nil {
				_ = file.Close()

				return nil, err
			}
		}
		f.quota.files[key] = state
	}
	if flag&os.O_TRUNC != 0 {
		f.quota.release(state.size)
		state.size = 0
	}
	state.openHandles++

	return &quotaFile{File: file, quota: f.quota, state: state, append: flag&os.O_APPEND != 0}, nil
}

func (f *quotaFilesystem) Create(filename string) (billy.File, error) {
	return f.OpenFile(filename, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o666)
}

func (f *quotaFilesystem) TempFile(directory, prefix string) (billy.File, error) {
	file, err := f.Filesystem.TempFile(directory, prefix)
	if err != nil {
		return nil, err
	}
	f.quota.mu.Lock()
	key := f.key(file.Name())
	state := &quotaFileState{linked: true, openHandles: 1}
	f.quota.files[key] = state
	f.quota.mu.Unlock()

	return &quotaFile{File: file, quota: f.quota, state: state}, nil
}

func (f *quotaFilesystem) Rename(oldpath, newpath string) error {
	f.quota.mu.Lock()
	defer f.quota.mu.Unlock()
	oldKey, newKey := f.key(oldpath), f.key(newpath)
	if oldKey == newKey {
		return nil
	}
	if err := f.Filesystem.Rename(oldpath, newpath); err != nil {
		return err
	}
	f.unlinkPath(newKey)
	for key, state := range f.quota.files {
		if key == oldKey || strings.HasPrefix(key, oldKey+"/") {
			moved := newKey + strings.TrimPrefix(key, oldKey)
			state.linked = true
			f.quota.files[moved] = state
			delete(f.quota.files, key)
		}
	}

	return nil
}

func (f *quotaFilesystem) Remove(filename string) error {
	f.quota.mu.Lock()
	defer f.quota.mu.Unlock()
	if err := f.Filesystem.Remove(filename); err != nil {
		return err
	}
	f.unlinkPath(f.key(filename))

	return nil
}

func (f *quotaFilesystem) Chroot(directory string) (billy.Filesystem, error) {
	filesystem, err := f.Filesystem.Chroot(directory)
	if err != nil {
		return nil, err
	}

	return &quotaFilesystem{
		Filesystem: filesystem,
		quota:      f.quota,
		prefix:     path.Join(f.prefix, directory),
	}, nil
}

func (f *quotaFilesystem) key(filename string) string {
	return path.Clean(path.Join(f.prefix, filename))
}

func (f *quotaFilesystem) unlinkPath(key string) {
	for filename, state := range f.quota.files {
		if filename == key || strings.HasPrefix(filename, key+"/") {
			state.linked = false
			if state.openHandles == 0 {
				f.quota.release(state.size)
				state.size = 0
			}
			delete(f.quota.files, filename)
		}
	}
}

func (q *filesystemQuota) reserve(size int64) error {
	if size < 0 || q.used > q.limit-size {
		return fmt.Errorf("%w (%d bytes)", errGitObjectStoreLimit, q.limit)
	}
	q.used += size

	return nil
}

func (q *filesystemQuota) release(size int64) {
	q.used -= size
}

type quotaFile struct {
	billy.File
	quota    *filesystemQuota
	state    *quotaFileState
	append   bool
	closed   bool
	closeErr error
}

func (f *quotaFile) Close() error {
	f.quota.mu.Lock()
	defer f.quota.mu.Unlock()
	if f.closed {
		return f.closeErr
	}
	f.closeErr = f.File.Close()
	f.closed = true
	f.state.openHandles--
	if !f.state.linked && f.state.openHandles == 0 {
		f.quota.release(f.state.size)
		f.state.size = 0
	}

	return f.closeErr
}

func (f *quotaFile) Write(buffer []byte) (int, error) {
	f.quota.mu.Lock()
	defer f.quota.mu.Unlock()

	offset := f.state.size
	if !f.append {
		var err error
		offset, err = f.File.Seek(0, io.SeekCurrent)
		if err != nil {
			return 0, err
		}
	}
	if offset < 0 || int64(len(buffer)) > int64(^uint64(0)>>1)-offset {
		return 0, errors.New("invalid Git object store file offset")
	}
	end := offset + int64(len(buffer))
	if growth := end - f.state.size; growth > 0 {
		if err := f.quota.reserve(growth); err != nil {
			return 0, err
		}
	}
	n, writeErr := f.File.Write(buffer)
	actualEnd := offset + int64(n)
	if actualEnd > f.state.size {
		f.quota.release(end - actualEnd)
		f.state.size = actualEnd
	} else if end > f.state.size {
		f.quota.release(end - f.state.size)
	}

	return n, writeErr
}

func (f *quotaFile) Truncate(size int64) error {
	f.quota.mu.Lock()
	defer f.quota.mu.Unlock()
	if size < 0 {
		return errors.New("invalid Git object store file size")
	}
	if size > f.state.size {
		if err := f.quota.reserve(size - f.state.size); err != nil {
			return err
		}
	}
	if err := f.File.Truncate(size); err != nil {
		if size > f.state.size {
			f.quota.release(size - f.state.size)
		}

		return err
	}
	if size < f.state.size {
		f.quota.release(f.state.size - size)
	}
	f.state.size = size

	return nil
}

func (f *quotaFile) Seek(offset int64, whence int) (int64, error) {
	f.quota.mu.Lock()
	defer f.quota.mu.Unlock()

	return f.File.Seek(offset, whence)
}
