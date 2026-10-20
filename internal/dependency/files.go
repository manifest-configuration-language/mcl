package dependency

import (
	"fmt"
	"io"
	"os"
)

func readLimitedFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxRemoteManifestSize+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxRemoteManifestSize {
		return nil, fmt.Errorf("file %q exceeds the %d-byte limit", path, maxRemoteManifestSize)
	}

	return data, nil
}

func readLimitedFileWithLimit(path string, maximumSize int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maximumSize+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximumSize {
		return nil, fmt.Errorf("file %q exceeds the %d-byte limit", path, maximumSize)
	}

	return data, nil
}
