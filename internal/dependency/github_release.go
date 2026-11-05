package dependency

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const githubAPIBaseURL = "https://api.github.com"

const maxGitHubReleaseMetadataSize = 1 << 20

type githubRelease struct {
	Assets []githubReleaseAsset `json:"assets"`
}

type githubReleaseAsset struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func fetchGitHubReleaseAsset(ctx context.Context, source, token string, transport http.RoundTripper, apiBaseURL string, maximumSize int64) ([]byte, bool, error) {
	owner, repository, tag, name, recognized := parseGitHubReleaseAssetURL(source)
	if !recognized || strings.TrimSpace(token) == "" {
		return nil, false, nil
	}

	apiBase, err := url.Parse(apiBaseURL)
	if err != nil || apiBase.Scheme != schemeHTTPS || apiBase.Host == "" || apiBase.User != nil || apiBase.Path != "" || apiBase.RawQuery != "" || apiBase.Fragment != "" {
		return nil, true, errors.New("invalid GitHub API base URL")
	}

	apiClient := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	releaseURL := githubAPIEndpoint(apiBase, "repos", owner, repository, "releases", "tags", tag)
	releaseRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseURL, nil)
	if err != nil {
		return nil, true, errors.New("create GitHub release metadata request")
	}
	setGitHubHeaders(releaseRequest, token, "application/vnd.github+json")
	releaseResponse, err := apiClient.Do(releaseRequest)
	if err != nil {
		return nil, true, fmt.Errorf("fetch GitHub release metadata: %w", err)
	}
	defer releaseResponse.Body.Close()
	if releaseResponse.StatusCode != http.StatusOK {
		return nil, true, fmt.Errorf("fetch GitHub release metadata: unexpected HTTP status %s", releaseResponse.Status)
	}
	metadata, err := readRemoteResponse(releaseResponse.Body, "GitHub release metadata", maxGitHubReleaseMetadataSize)
	if err != nil {
		return nil, true, err
	}
	var release githubRelease
	if err := json.Unmarshal(metadata, &release, json.RejectUnknownMembers(false)); err != nil {
		return nil, true, fmt.Errorf("decode GitHub release metadata: %w", err)
	}
	var assetID int64
	for _, asset := range release.Assets {
		if asset.Name == name {
			assetID = asset.ID

			break
		}
	}
	if assetID <= 0 {
		return nil, true, fmt.Errorf("GitHub release does not contain asset %q", name)
	}

	assetURL := githubAPIEndpoint(apiBase, "repos", owner, repository, "releases", "assets", strconv.FormatInt(assetID, 10))
	assetRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return nil, true, errors.New("create GitHub release asset request")
	}
	setGitHubHeaders(assetRequest, token, "application/octet-stream")
	assetClient := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if err := checkRemoteRedirect(request, via); err != nil {
				return err
			}
			if request.URL.Host != apiBase.Host {
				request.Header.Del("Authorization")
			}

			return nil
		},
	}
	assetResponse, err := assetClient.Do(assetRequest)
	if err != nil {
		return nil, true, fmt.Errorf("fetch GitHub release asset: %w", err)
	}
	defer assetResponse.Body.Close()
	if assetResponse.StatusCode != http.StatusOK {
		return nil, true, fmt.Errorf("fetch GitHub release asset: unexpected HTTP status %s", assetResponse.Status)
	}
	data, err := readRemoteResponse(assetResponse.Body, "GitHub release asset", maximumSize)
	if err != nil {
		return nil, true, err
	}

	return data, true, nil
}

func githubAPIEndpoint(base *url.URL, segments ...string) string {
	escapedSegments := make([]string, len(segments))
	for index, segment := range segments {
		escapedSegments[index] = url.PathEscape(segment)
	}
	endpoint := base.Clone()
	endpoint.Path = "/" + strings.Join(segments, "/")
	endpoint.RawPath = "/" + strings.Join(escapedSegments, "/")

	return endpoint.String()
}

func setGitHubHeaders(request *http.Request, token, accept string) {
	request.Header.Set("Accept", accept)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "mcl")
}

func parseGitHubReleaseAssetURL(source string) (owner, repository, tag, name string, ok bool) {
	parsed, err := url.Parse(source)
	if err != nil || parsed.Scheme != schemeHTTPS || parsed.Host != "github.com" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", "", "", "", false
	}
	escapedPath := parsed.EscapedPath()
	if !strings.HasPrefix(escapedPath, "/") || strings.HasPrefix(escapedPath, "//") || strings.HasSuffix(escapedPath, "/") {
		return "", "", "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(escapedPath, "/"), "/")
	if len(parts) != 6 || parts[2] != "releases" || parts[3] != "download" {
		return "", "", "", "", false
	}
	decodedParts := make([]string, len(parts))
	for index, part := range parts {
		decoded, err := url.PathUnescape(part)
		if err != nil || decoded == "" || decoded == "." || decoded == ".." || strings.Contains(decoded, "\\") {
			return "", "", "", "", false
		}
		if index != 4 && strings.Contains(decoded, "/") {
			return "", "", "", "", false
		}
		decodedParts[index] = decoded
	}

	return decodedParts[0], decodedParts[1], decodedParts[4], decodedParts[5], true
}
