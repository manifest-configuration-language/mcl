package dependency

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestGitFetchLimitTransport(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name         string
		body         string
		want         string
		wantLimitErr bool
	}{
		{name: "at limit", body: "data", want: "data"},
		{name: "over limit", body: "data!", wantLimitErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			transport := &gitFetchLimitTransport{
				base: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{Body: io.NopCloser(strings.NewReader(test.body))}, nil
				}),
				limit: 4,
			}
			response, err := transport.RoundTrip(new(http.Request))
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()

			body, err := io.ReadAll(response.Body)
			if test.wantLimitErr {
				if !errors.Is(err, errGitFetchResponseLimit) {
					t.Fatalf("ReadAll() error = %v, want %v", err, errGitFetchResponseLimit)
				}

				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != test.want {
				t.Fatalf("ReadAll() = %q, want %q", body, test.want)
			}
		})
	}
}

func TestQuotaFilesystemEnforcesLimit(t *testing.T) {
	t.Parallel()

	filesystem := newQuotaFilesystem(memfs.New(), 4)
	file, err := filesystem.OpenFile("object", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	if n, err := file.Write([]byte("data")); err != nil || n != 4 {
		t.Fatalf("Write() = %d, %v; want 4, nil", n, err)
	}
	if n, err := file.Write([]byte("!")); n != 0 || !errors.Is(err, errGitObjectStoreLimit) {
		t.Fatalf("Write() over limit = %d, %v; want 0 and %v", n, err, errGitObjectStoreLimit)
	}
	if filesystem.quota.used != 4 {
		t.Fatalf("quota usage = %d, want 4", filesystem.quota.used)
	}
}
