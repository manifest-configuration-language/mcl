package dependency

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type releaseRoundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip releaseRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestParseGitHubReleaseAssetURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		source     string
		wantOwner  string
		wantRepo   string
		wantTag    string
		wantAsset  string
		wantParsed bool
	}{
		{
			name:       "release asset",
			source:     "https://github.com/walnuts1018/cluster-api-provider-tart/releases/download/v0.3.16/infrastructure-components.yaml",
			wantOwner:  "walnuts1018",
			wantRepo:   "cluster-api-provider-tart",
			wantTag:    "v0.3.16",
			wantAsset:  "infrastructure-components.yaml",
			wantParsed: true,
		},
		{
			name:       "slash in tag",
			source:     "https://github.com/example/repo/releases/download/release%2Fv1/schema.yaml",
			wantOwner:  "example",
			wantRepo:   "repo",
			wantTag:    "release/v1",
			wantAsset:  "schema.yaml",
			wantParsed: true,
		},
		{
			name:   "non-release URL",
			source: "https://raw.githubusercontent.com/example/repo/main/file.yaml",
		},
		{
			name:   "query is not canonical",
			source: "https://github.com/example/repo/releases/download/v1/file.yaml?download=1",
		},
		{
			name:   "encoded path separator",
			source: "https://github.com/example/repo/releases/download/v1/file%2Fother.yaml",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			owner, repository, tag, asset, ok := parseGitHubReleaseAssetURL(test.source)
			if ok != test.wantParsed || owner != test.wantOwner || repository != test.wantRepo || tag != test.wantTag || asset != test.wantAsset {
				t.Fatalf("parseGitHubReleaseAssetURL() = (%q, %q, %q, %q, %t)", owner, repository, tag, asset, ok)
			}
		})
	}
}

func TestFetchGitHubReleaseAssetDoesNotForwardAuthorizationOnRedirect(t *testing.T) {
	t.Parallel()

	var requests []*http.Request
	transport := releaseRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request)
		response := &http.Response{Header: make(http.Header), Request: request}
		switch request.URL.Host {
		case "api.github.com":
			switch request.URL.Path {
			case "/repos/example/private/releases/tags/v1.2.3":
				response.StatusCode = http.StatusOK
				response.Body = io.NopCloser(strings.NewReader(`{"assets":[{"id":42,"name":"schema.yaml"}]}`))
			case "/repos/example/private/releases/assets/42":
				response.StatusCode = http.StatusFound
				response.Header.Set("Location", "https://release-assets.githubusercontent.com/schema.yaml")
				response.Body = io.NopCloser(strings.NewReader(""))
			default:
				t.Fatalf("unexpected API path %q", request.URL.Path)
			}
		case "release-assets.githubusercontent.com":
			response.StatusCode = http.StatusOK
			response.Body = io.NopCloser(strings.NewReader("schema bytes"))
		default:
			t.Fatalf("unexpected request host %q", request.URL.Host)
		}

		return response, nil
	})

	data, handled, err := fetchGitHubReleaseAsset(
		t.Context(),
		"https://github.com/example/private/releases/download/v1.2.3/schema.yaml",
		"test-token",
		transport,
		githubAPIBaseURL,
		maxRemoteManifestSize,
	)
	if err != nil {
		t.Fatalf("fetchGitHubReleaseAsset() error = %v", err)
	}
	if !handled {
		t.Fatal("fetchGitHubReleaseAsset() did not handle a canonical release asset URL")
	}
	if string(data) != "schema bytes" {
		t.Fatalf("fetchGitHubReleaseAsset() data = %q", data)
	}
	if len(requests) != 3 {
		t.Fatalf("got %d requests, want 3", len(requests))
	}
	for _, request := range requests[:2] {
		if request.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("API request Authorization = %q", request.Header.Get("Authorization"))
		}
	}
	if accept := requests[1].Header.Get("Accept"); accept != "application/octet-stream" {
		t.Fatalf("asset API Accept = %q", accept)
	}
	if authorization := requests[2].Header.Get("Authorization"); authorization != "" {
		t.Fatalf("redirected storage request carried Authorization %q", authorization)
	}
}

func TestFetchGitHubReleaseAssetWithoutTokenFallsBack(t *testing.T) {
	t.Parallel()

	data, handled, err := fetchGitHubReleaseAsset(
		t.Context(),
		"https://github.com/example/private/releases/download/v1.2.3/schema.yaml",
		" ",
		releaseRoundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("transport called without a token")

			return nil, nil
		}),
		githubAPIBaseURL,
		maxRemoteManifestSize,
	)
	if err != nil || handled || data != nil {
		t.Fatalf("fetchGitHubReleaseAsset() = (%q, %t, %v), want (nil, false, nil)", data, handled, err)
	}
}
