package content

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func SHA256(data []byte) string {
	digest := sha256.Sum256(data)

	return hex.EncodeToString(digest[:])
}

func IsSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)

	return err == nil
}

func WriteFileAtomic(path string, data []byte) error {
	directory := filepath.Dir(path)
	file, err := os.CreateTemp(directory, ".mcl-tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(0o644); err != nil {
		file.Close()

		return fmt.Errorf("set temporary file permissions: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()

		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()

		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("replace %s: %w", filepath.Base(path), err)
	}

	return nil
}

// WriteFileAtomicRoot replaces name with complete content using paths relative to root.
func WriteFileAtomicRoot(root *os.Root, name string, data []byte) (err error) {
	if !fs.ValidPath(name) || name == "." {
		return fs.ErrInvalid
	}
	directory := filepath.Dir(name)
	base := filepath.Base(name)
	var temporary string
	var file *os.File
	for range 10 {
		suffix := make([]byte, 12)
		if _, err := rand.Read(suffix); err != nil {
			return fmt.Errorf("generate temporary file name: %w", err)
		}
		temporary = ".mcl-tmp-" + hex.EncodeToString(suffix)
		if directory != "." {
			temporary = filepath.Join(directory, temporary)
		}
		file, err = root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if !errors.Is(err, fs.ErrExist) {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	defer func() {
		_ = root.Remove(temporary)
	}()
	if err := file.Chmod(0o644); err != nil {
		_ = file.Close()

		return fmt.Errorf("set temporary file permissions: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()

		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()

		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err := root.Rename(temporary, name); err != nil {
		return fmt.Errorf("replace %s: %w", base, err)
	}

	return nil
}
