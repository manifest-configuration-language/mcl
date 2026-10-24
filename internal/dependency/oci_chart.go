package dependency

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/manifest-configuration-language/mcl/internal/content"
	"github.com/manifest-configuration-language/mcl/internal/netpolicy"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	orascontent "oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
)

const (
	helmChartLayerMediaType       = "application/vnd.cncf.helm.chart.content.v1.tar+gzip"
	helmLegacyChartLayerMediaType = "application/tar+gzip"
)

type httpsOnlyTransport struct {
	base http.RoundTripper
}

func (transport httpsOnlyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != schemeHTTPS {
		return nil, errors.New("OCI registry requests must use HTTPS")
	}

	return transport.base.RoundTrip(request)
}

func ValidateOCIChartSource(source string) error {
	parsed, err := url.Parse(source)
	if err != nil {
		return fmt.Errorf("invalid OCI Chart reference: %w", err)
	}
	if parsed.Scheme != "oci" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return errors.New("OCI Chart reference must be oci://host/repository/chart without credentials, query, or fragment")
	}
	if strings.Contains(parsed.Path, "\\") || strings.Contains(parsed.Path, "//") {
		return errors.New("invalid OCI Chart repository path")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 2 || slices.Contains(parts, "") || slices.Contains(parts, ".") || slices.Contains(parts, "..") {
		return errors.New("OCI Chart reference must include a registry and repository path")
	}
	if strings.ContainsAny(parts[len(parts)-1], ":@") {
		return errors.New("OCI Chart reference must not include a tag or digest")
	}

	return nil
}

func fetchOCIChart(ctx context.Context, source, version string) ([]byte, ociChartLock, error) {
	var empty ociChartLock
	if err := ValidateOCIChartSource(source); err != nil {
		return nil, empty, err
	}
	if version == "" || strings.ContainsAny(version, "/:@") {
		return nil, empty, fmt.Errorf("invalid OCI Helm Chart version %q", version)
	}
	transport, err := netpolicy.NewExternalHTTPTransport()
	if err != nil {
		return nil, empty, err
	}
	client := &http.Client{
		Transport: httpsOnlyTransport{base: transport},
		Timeout:   30 * time.Second,
		CheckRedirect: func(request *http.Request, _ []*http.Request) error {
			if request.URL.Scheme != schemeHTTPS {
				return errors.New("OCI registry redirects must use HTTPS")
			}

			return nil
		},
	}
	credentialClient := &auth.Client{
		Client: client,
		Credential: func(context.Context, string) (auth.Credential, error) {
			return auth.EmptyCredential, nil
		},
		Cache: auth.NewCache(),
	}
	repository, err := remote.NewRepository(strings.TrimPrefix(source, "oci://"))
	if err != nil {
		return nil, empty, fmt.Errorf("parse OCI Helm Chart reference: %w", err)
	}
	repository.Client = credentialClient
	repository.PlainHTTP = false
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tag := strings.ReplaceAll(version, "+", "_")
	manifestDescriptor, err := repository.Resolve(ctx, tag)
	if err != nil {
		return nil, empty, fmt.Errorf("resolve OCI Helm Chart version %q: %w", version, err)
	}
	if !validOCIDigest(string(manifestDescriptor.Digest)) {
		return nil, empty, fmt.Errorf("OCI registry returned an unsupported manifest digest %q", manifestDescriptor.Digest)
	}
	if manifestDescriptor.Size < 0 || manifestDescriptor.Size > 1<<20 {
		return nil, empty, fmt.Errorf("OCI Helm Chart manifest exceeds %d bytes", 1<<20)
	}
	manifestReader, err := repository.Fetch(ctx, manifestDescriptor)
	if err != nil {
		return nil, empty, fmt.Errorf("fetch OCI Helm Chart manifest: %w", err)
	}
	manifestData, err := readOCIContent(manifestReader, manifestDescriptor, "OCI manifest")
	if err != nil {
		return nil, empty, err
	}
	var manifest v1.Manifest
	if err := json.Unmarshal(manifestData, &manifest, json.RejectUnknownMembers(false)); err != nil {
		return nil, empty, fmt.Errorf("decode OCI Helm Chart manifest: %w", err)
	}
	if manifest.SchemaVersion != 2 {
		return nil, empty, fmt.Errorf("unsupported OCI Helm Chart manifest schema version %d", manifest.SchemaVersion)
	}
	var layer *v1.Descriptor
	for index := range manifest.Layers {
		candidate := &manifest.Layers[index]
		if candidate.MediaType == helmChartLayerMediaType || candidate.MediaType == helmLegacyChartLayerMediaType {
			if layer != nil {
				return nil, empty, errors.New("OCI Helm Chart manifest contains multiple chart layers")
			}
			layer = candidate
		}
	}
	if layer == nil {
		return nil, empty, errors.New("OCI manifest does not contain a Helm Chart content layer")
	}
	if !validOCIDigest(string(layer.Digest)) {
		return nil, empty, fmt.Errorf("OCI Helm Chart layer has unsupported digest %q", layer.Digest)
	}
	if layer.Size < 0 || layer.Size > maxRemoteChartArchiveSize {
		return nil, empty, fmt.Errorf("OCI Helm Chart layer exceeds %d bytes", maxRemoteChartArchiveSize)
	}
	layerReader, err := repository.Fetch(ctx, *layer)
	if err != nil {
		return nil, empty, fmt.Errorf("fetch OCI Helm Chart content layer: %w", err)
	}
	archive, err := readOCIContent(layerReader, *layer, "OCI Helm Chart layer")
	if err != nil {
		return nil, empty, err
	}

	return archive, ociChartLock{
		ManifestDigest:   string(manifestDescriptor.Digest),
		ManifestSize:     manifestDescriptor.Size,
		ChartLayerDigest: string(layer.Digest),
		ChartLayerSize:   layer.Size,
		ArchiveDigest:    content.SHA256(archive),
	}, nil
}

func readOCIContent(reader io.ReadCloser, descriptor v1.Descriptor, label string) ([]byte, error) {
	defer reader.Close()
	data, err := orascontent.ReadAll(reader, descriptor)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", label, err)
	}

	return data, nil
}
