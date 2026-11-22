package dependency

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFetchGitTreeWithCredentialsFromPrivateRepository(t *testing.T) {
	t.Parallel()

	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is required to run the private Git HTTP integration test")
	}

	projectRoot := t.TempDir()
	repositoryPath := filepath.Join(projectRoot, "repository")
	runGit(t, gitPath, "init", repositoryPath)
	runGit(t, gitPath, "-C", repositoryPath, "config", "user.name", "MCL test")
	runGit(t, gitPath, "-C", repositoryPath, "config", "user.email", "mcl-test@example.invalid")
	runGit(t, gitPath, "-C", repositoryPath, "config", "http.uploadpack", "true")
	runGit(t, gitPath, "-C", repositoryPath, "config", "uploadpack.allowTipSHA1InWant", "true")
	directory := filepath.Join(repositoryPath, "config", "base")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	const manifest = "kind: ConfigMap\nmetadata:\n  name: private-source\n"
	if err := os.WriteFile(filepath.Join(directory, "kustomization.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, gitPath, "-C", repositoryPath, "add", "config/base/kustomization.yaml")
	runGit(t, gitPath, "-C", repositoryPath, "commit", "-m", "add private dependency")
	revision := strings.TrimSpace(runGit(t, gitPath, "-C", repositoryPath, "rev-parse", "HEAD"))

	execPath := strings.TrimSpace(runGit(t, gitPath, "--exec-path"))
	var authorized atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		username, password, ok := request.BasicAuth()
		if ok && username == "git-user" && password == "git-password" {
			authorized.Add(1)
			serveGitHTTPBackend(writer, request, projectRoot, execPath)

			return
		}
		writer.Header().Set("WWW-Authenticate", `Basic realm="private Git repository"`)
		writer.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	provider := func(_ context.Context, source string) (GitCredentials, error) {
		if source != server.URL+"/repository" {
			t.Fatalf("credential provider source = %q, want %q", source, server.URL+"/repository")
		}

		return GitCredentials{Username: "git-user", Password: "git-password"}, nil
	}
	archive, err := fetchGitTreeWithCredentialsUsingTransportFactory(
		t.Context(),
		"git+"+server.URL+"/repository",
		revision,
		"config/base",
		provider,
		func() (http.RoundTripper, error) {
			return server.Client().Transport, nil
		},
	)
	if err != nil {
		t.Fatalf("fetchGitTreeWithCredentialsUsingTransportFactory() error = %v", err)
	}
	if authorized.Load() == 0 {
		t.Fatal("private Git server did not receive authenticated requests")
	}
	reader := tar.NewReader(bytes.NewReader(archive))
	header, err := reader.Next()
	if err != nil {
		t.Fatalf("read fetched Git tree archive: %v", err)
	}
	if header.Name != "kustomization.yaml" {
		t.Fatalf("fetched Git tree entry = %q, want kustomization.yaml", header.Name)
	}
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read fetched Git tree file: %v", err)
	}
	if string(got) != manifest {
		t.Fatalf("fetched Git tree content = %q, want %q", got, manifest)
	}
	if _, err := reader.Next(); err != io.EOF {
		t.Fatalf("fetched Git tree has unexpected additional entries, next error = %v", err)
	}
}

func runGit(t *testing.T, gitPath string, args ...string) string {
	t.Helper()
	command := exec.CommandContext(t.Context(), gitPath, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}

	return string(output)
}

func serveGitHTTPBackend(writer http.ResponseWriter, request *http.Request, projectRoot, execPath string) {
	serverName, serverPort, err := net.SplitHostPort(request.Host)
	if err != nil {
		http.Error(writer, "invalid Host header", http.StatusBadRequest)

		return
	}
	command := exec.Command("git", "http-backend")
	command.Env = append(os.Environ(),
		"GIT_HTTP_EXPORT_ALL=1",
		"GIT_PROJECT_ROOT="+projectRoot,
		"GIT_EXEC_PATH="+execPath,
		"SERVER_SOFTWARE=go-test",
		"SERVER_PROTOCOL="+request.Proto,
		"HTTP_HOST="+request.Host,
		"GATEWAY_INTERFACE=CGI/1.1",
		"REQUEST_METHOD="+request.Method,
		"QUERY_STRING="+request.URL.RawQuery,
		"REQUEST_URI="+request.URL.RequestURI(),
		"PATH_INFO="+request.URL.Path,
		"SCRIPT_FILENAME="+filepath.Join(execPath, "git-http-backend"),
		"SERVER_NAME="+serverName,
		"SERVER_PORT="+serverPort,
		"REMOTE_ADDR=127.0.0.1",
		"HTTPS=on",
	)
	if request.ContentLength > 0 {
		command.Env = append(command.Env, "CONTENT_LENGTH="+strconv.FormatInt(request.ContentLength, 10))
	}
	if contentType := request.Header.Get("Content-Type"); contentType != "" {
		command.Env = append(command.Env, "CONTENT_TYPE="+contentType)
	}
	if gitProtocol := request.Header.Get("Git-Protocol"); gitProtocol != "" {
		command.Env = append(command.Env, "HTTP_GIT_PROTOCOL="+gitProtocol)
	}
	command.Stdin = request.Body
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		http.Error(writer, stderr.String(), http.StatusInternalServerError)

		return
	}
	reader := bufio.NewReader(bytes.NewReader(output))
	statusCode := http.StatusOK
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			http.Error(writer, "invalid CGI response", http.StatusInternalServerError)

			return
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			http.Error(writer, "invalid CGI response header", http.StatusInternalServerError)

			return
		}
		value = strings.TrimSpace(value)
		if name == "Status" {
			status, _, _ := strings.Cut(value, " ")
			statusCode, err = strconv.Atoi(status)
			if err != nil {
				http.Error(writer, "invalid CGI response status", http.StatusInternalServerError)

				return
			}

			continue
		}
		writer.Header().Add(name, value)
	}
	writer.WriteHeader(statusCode)
	_, _ = io.Copy(writer, reader)
}

func TestValidateGitSource(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		source string
		valid  bool
	}{
		{name: "HTTPS repository", source: "git+https://example.test/team/repo.git", valid: true},
		{name: "missing prefix", source: "https://example.test/team/repo.git"},
		{name: "insecure scheme", source: "git+http://example.test/team/repo.git"},
		{name: "userinfo", source: "git+https://user@example.test/team/repo.git"},
		{name: "query", source: "git+https://example.test/team/repo.git?token=x"},
		{name: "fragment", source: "git+https://example.test/team/repo.git#main"},
		{name: "missing host", source: "git+https:///team/repo.git"},
		{name: "backslash path", source: "git+https://example.test/team\\repo.git"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateGitSource(test.source)
			if (err == nil) != test.valid {
				t.Fatalf("ValidateGitSource(%q) error = %v, valid = %t", test.source, err, test.valid)
			}
		})
	}
}

func TestValidGitRevision(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		revision string
		valid    bool
	}{
		{name: "lowercase SHA-1", revision: strings.Repeat("a", 40), valid: true},
		{name: "uppercase", revision: "A" + strings.Repeat("a", 39)},
		{name: "short", revision: strings.Repeat("a", 39)},
		{name: "SHA-256 length", revision: strings.Repeat("a", 64)},
		{name: "non-hex", revision: strings.Repeat("g", 40)},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if valid := ValidGitRevision(test.revision); valid != test.valid {
				t.Fatalf("ValidGitRevision(%q) = %t, want %t", test.revision, valid, test.valid)
			}
		})
	}
}

func TestValidateGitDirectory(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		directory string
		valid     bool
	}{
		{name: "repository root", directory: ".", valid: true},
		{name: "nested directory", directory: "config/base", valid: true},
		{name: "empty", directory: ""},
		{name: "parent traversal", directory: "../outside"},
		{name: "embedded traversal", directory: "base/../outside"},
		{name: "absolute", directory: "/etc"},
		{name: "windows separator", directory: `base\outside`},
		{name: "duplicate separator", directory: "base//outside"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateGitDirectory(test.directory)
			if (err == nil) != test.valid {
				t.Fatalf("ValidateGitDirectory(%q) error = %v, valid = %t", test.directory, err, test.valid)
			}
		})
	}
}
