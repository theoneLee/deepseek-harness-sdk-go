package deepseekharness

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// SDKVersion is the default runtime release targeted by this SDK. It tracks
// the version portion of the upstream dsh-v0.1.1-rc.2 tag and is converted to
// PEP 440 for index requests.
const SDKVersion = "0.1.1-rc.2"

const runtimeWheelMaxBytes int64 = 512 * 1024 * 1024

// ResolveOptions controls bundled runtime acquisition.
type ResolveOptions struct {
	IndexURL string
	Version  string
	CacheDir string
}

// ResolvedRuntime contains the executable argv and extracted default Cordis
// configuration selected for the current platform.
type ResolvedRuntime struct {
	LaunchArgs    []string
	DefaultConfig string
}

// ToPyPIVersion converts semver prerelease spelling to the spelling used by
// PyPI wheel indexes.
func ToPyPIVersion(version string) string {
	for _, replacement := range []struct{ from, to string }{
		{"-rc.", "rc"},
		{"-beta.", "b"},
		{"-alpha.", "a"},
	} {
		if index := strings.Index(version, replacement.from); index >= 0 {
			return version[:index] + replacement.to + version[index+len(replacement.from):]
		}
	}
	return version
}

// ResolveRuntime reuses a valid cache entry or downloads, verifies, and
// extracts the platform runtime wheel from a PyPI-compatible index.
func ResolveRuntime(options ResolveOptions) (ResolvedRuntime, error) {
	wheelTag, executableTag, err := currentPlatform()
	if err != nil {
		return ResolvedRuntime{}, err
	}
	version := options.Version
	if version == "" {
		version = os.Getenv("DSH_RUNTIME_VERSION")
	}
	if version == "" {
		version = SDKVersion
	}
	version = ToPyPIVersion(version)
	indexURL := options.IndexURL
	if indexURL == "" {
		indexURL = os.Getenv("DSH_RUNTIME_PYPI_URL")
	}
	if indexURL == "" {
		indexURL = "https://pypi.org/pypi"
	}
	indexURL = strings.TrimRight(indexURL, "/")
	cacheRoot := options.CacheDir
	if cacheRoot == "" {
		cacheRoot = os.Getenv("DSH_RUNTIME_CACHE_DIR")
	}
	if cacheRoot == "" {
		userCache, cacheErr := os.UserCacheDir()
		if cacheErr != nil {
			return ResolvedRuntime{}, runtimeResolveError("no cache directory for the runtime download; set $DSH_RUNTIME_CACHE_DIR")
		}
		cacheRoot = filepath.Join(userCache, "deepseek-harness-sdk-go")
	}

	cacheDir := filepath.Join(cacheRoot, version)
	executableName := "dsh-jsonrpc-agent-pkg-" + executableTag
	executablePath := filepath.Join(cacheDir, executableName)
	defaultConfig := filepath.Join(cacheDir, "cordis.yml")
	if cacheValid(cacheDir, executablePath, executableName) {
		return ResolvedRuntime{
			LaunchArgs:    []string{executablePath},
			DefaultConfig: defaultConfig,
		}, nil
	}

	wheel, err := fetchWheel(indexURL, version, wheelTag)
	if err != nil {
		return ResolvedRuntime{}, err
	}
	wheelBytes, err := downloadWheel(wheel.URL)
	if err != nil {
		return ResolvedRuntime{}, err
	}
	if err := verifySHA256(wheelBytes, wheel.SHA256); err != nil {
		return ResolvedRuntime{}, err
	}
	if err := extractRuntime(cacheDir, executableName, wheelBytes); err != nil {
		return ResolvedRuntime{}, err
	}
	return ResolvedRuntime{
		LaunchArgs:    []string{executablePath},
		DefaultConfig: defaultConfig,
	}, nil
}

func currentPlatform() (wheelTag, executableTag string, err error) {
	switch {
	case runtime.GOOS == "darwin" && runtime.GOARCH == "arm64":
		return "py3-none-macosx_14_0_arm64", "macos-arm64", nil
	case runtime.GOOS == "linux" && runtime.GOARCH == "amd64":
		return "py3-none-manylinux_2_28_x86_64", "linux-x64", nil
	case runtime.GOOS == "linux" && runtime.GOARCH == "arm64":
		return "py3-none-manylinux_2_28_aarch64", "linux-arm64", nil
	default:
		return "", "", runtimeResolveError(
			"no deepseek-harness-runtime-bin wheel exists for this platform (os=%s, arch=%s); supported: macos/arm64, linux/x64, linux/arm64. Provide HarnessClientOptions.RuntimeBin instead",
			runtime.GOOS, runtime.GOARCH,
		)
	}
}

type wheelFile struct {
	URL    string
	SHA256 string
}

type pypiMetadata struct {
	URLs []struct {
		Filename string `json:"filename"`
		URL      string `json:"url"`
		Digests  struct {
			SHA256 string `json:"sha256"`
		} `json:"digests"`
	} `json:"urls"`
}

func fetchWheel(indexURL, version, wheelTag string) (wheelFile, error) {
	requestURL := fmt.Sprintf("%s/deepseek-harness-runtime-bin/%s/json", indexURL, version)
	response, err := http.Get(requestURL)
	if err != nil {
		return wheelFile{}, runtimeResolveError("failed to fetch runtime index %s: %v", requestURL, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return wheelFile{}, runtimeResolveError("failed to fetch runtime index %s: HTTP %s", requestURL, response.Status)
	}
	var metadata pypiMetadata
	if err := json.NewDecoder(response.Body).Decode(&metadata); err != nil {
		return wheelFile{}, runtimeResolveError("runtime index %s is not valid JSON: %v", requestURL, err)
	}
	suffix := wheelTag + ".whl"
	for _, entry := range metadata.URLs {
		if !strings.HasSuffix(entry.Filename, suffix) {
			continue
		}
		if entry.URL == "" {
			return wheelFile{}, runtimeResolveError("wheel entry %s has no url", entry.Filename)
		}
		if entry.Digests.SHA256 == "" {
			return wheelFile{}, runtimeResolveError("wheel entry %s has no sha256 digest", entry.Filename)
		}
		return wheelFile{URL: entry.URL, SHA256: entry.Digests.SHA256}, nil
	}
	return wheelFile{}, runtimeResolveError(
		"deepseek-harness-runtime-bin %s has no wheel for platform tag %s; provide HarnessClientOptions.RuntimeBin instead",
		version, wheelTag,
	)
}

func downloadWheel(url string) ([]byte, error) {
	response, err := http.Get(url)
	if err != nil {
		return nil, runtimeResolveError("failed to download runtime wheel %s: %v", url, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, runtimeResolveError("failed to download runtime wheel %s: HTTP %s", url, response.Status)
	}
	limited := io.LimitReader(response.Body, runtimeWheelMaxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, runtimeResolveError("failed to read runtime wheel %s: %v", url, err)
	}
	if int64(len(data)) > runtimeWheelMaxBytes {
		return nil, runtimeResolveError("runtime wheel %s exceeds the %d-byte limit", url, runtimeWheelMaxBytes)
	}
	return data, nil
}

func verifySHA256(data []byte, expectedHex string) error {
	expected, err := hex.DecodeString(expectedHex)
	if err != nil {
		return runtimeResolveError("invalid sha256 digest %q: %v", expectedHex, err)
	}
	actual := sha256.Sum256(data)
	if !equalBytes(actual[:], expected) {
		return runtimeResolveError("downloaded runtime wheel failed its sha256 verification")
	}
	return nil
}

func extractRuntime(cacheDir, executableName string, data []byte) error {
	staging := cacheDir + fmt.Sprintf(".tmp-%d", os.Getpid())
	if err := os.RemoveAll(staging); err != nil {
		return &IOError{Err: err}
	}
	cleanup := func() { _ = os.RemoveAll(staging) }
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return &IOError{Err: err}
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		cleanup()
		return runtimeResolveError("runtime wheel is not a readable zip: %v", err)
	}
	write := func(entryName, destination string, executable bool) error {
		for _, entry := range archive.File {
			if entry.Name != entryName {
				continue
			}
			reader, err := entry.Open()
			if err != nil {
				return &IOError{Err: err}
			}
			mode := os.FileMode(0o644)
			if executable {
				mode = 0o755
			}
			file, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				_ = reader.Close()
				return &IOError{Err: err}
			}
			_, copyErr := io.Copy(file, reader)
			closeFileErr := file.Close()
			closeReaderErr := reader.Close()
			if copyErr != nil {
				return &IOError{Err: copyErr}
			}
			if closeFileErr != nil {
				return &IOError{Err: closeFileErr}
			}
			if closeReaderErr != nil {
				return &IOError{Err: closeReaderErr}
			}
			return nil
		}
		return runtimeResolveError("runtime wheel is missing %s", entryName)
	}

	root := "deepseek_harness_runtime/runtime/"
	if err := write(root+executableName, filepath.Join(staging, executableName), true); err != nil {
		cleanup()
		return err
	}
	if strings.HasPrefix(executableName, "dsh-jsonrpc-agent-pkg-macos-") {
		helper := executableName + "-spawn-helper"
		if err := write(root+helper, filepath.Join(staging, helper), true); err != nil {
			cleanup()
			return err
		}
	}
	if err := write(root+"cordis.yml", filepath.Join(staging, "cordis.yml"), false); err != nil {
		cleanup()
		return err
	}
	marker, err := os.OpenFile(filepath.Join(staging, ".complete"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		cleanup()
		return &IOError{Err: err}
	}
	if err := marker.Close(); err != nil {
		cleanup()
		return &IOError{Err: err}
	}
	if err := os.RemoveAll(cacheDir); err != nil {
		cleanup()
		return &IOError{Err: err}
	}
	if err := os.MkdirAll(filepath.Dir(cacheDir), 0o755); err != nil {
		cleanup()
		return &IOError{Err: err}
	}
	if err := os.Rename(staging, cacheDir); err != nil {
		cleanup()
		return &IOError{Err: err}
	}
	return nil
}

func cacheValid(cacheDir, executablePath, executableName string) bool {
	if !isFile(filepath.Join(cacheDir, ".complete")) || !isFile(executablePath) {
		return false
	}
	if strings.HasPrefix(executableName, "dsh-jsonrpc-agent-pkg-macos-") {
		return isFile(executablePath + "-spawn-helper")
	}
	return true
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var difference byte
	for index := range left {
		difference |= left[index] ^ right[index]
	}
	return difference == 0
}
