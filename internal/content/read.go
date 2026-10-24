package content

import (
	"errors"
	"io"
)

var errNegativeMaximum = errors.New("maximum size must be non-negative")

// ReadAllLimited reads at most maximum bytes and reports whether more data was
// available.
func ReadAllLimited(reader io.Reader, maximum int64) ([]byte, bool, error) {
	if maximum < 0 {
		return nil, false, errNegativeMaximum
	}
	data, err := io.ReadAll(io.LimitReader(reader, maximum))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) < maximum {
		return data, false, nil
	}
	var probe [1]byte
	n, err := io.ReadFull(reader, probe[:])
	if n > 0 {
		return nil, true, nil
	}
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, false, err
	}

	return data, false, nil
}
