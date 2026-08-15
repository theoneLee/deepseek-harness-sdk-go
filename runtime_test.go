package deepseekharness

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func TestResolveRuntimeCachesVerifiedWheel(t *testing.T) {
	wheelTag, executableTag, err := currentPlatform()
	if err != nil {
		t.Skip(err)
	}
	executableName := "dsh-jsonrpc-agent-pkg-" + executableTag
	wheelBytes := makeTestWheel(t, executableName)
	digest := sha256.Sum256(wheelBytes)
	var indexRequests atomic.Int32
	var wheelRequests atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/json") {
			indexRequests.Add(1)
			_ = json.NewEncoder(writer).Encode(map[string]any{"urls": []any{
				map[string]any{
					"filename": "deepseek-harness-runtime-bin-1.2.3rc1-" + wheelTag + ".whl",
					"url":      server.URL + "/wheel.whl",
					"digests":  map[string]any{"sha256": hex.EncodeToString(digest[:])},
				},
			}})
			return
		}
		if request.URL.Path == "/wheel.whl" {
			wheelRequests.Add(1)
			_, _ = writer.Write(wheelBytes)
			return
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()

	cacheDir := t.TempDir()
	resolved, err := ResolveRuntime(ResolveOptions{
		IndexURL: server.URL,
		Version:  "1.2.3-rc.1",
		CacheDir: cacheDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.LaunchArgs) != 1 || !strings.HasSuffix(resolved.LaunchArgs[0], executableName) {
		t.Fatalf("resolved launch args: %#v", resolved.LaunchArgs)
	}
	if content, err := os.ReadFile(resolved.LaunchArgs[0]); err != nil || string(content) != "runtime" {
		t.Fatalf("extracted executable = %q, err=%v", content, err)
	}
	if content, err := os.ReadFile(resolved.DefaultConfig); err != nil || string(content) != "config" {
		t.Fatalf("extracted config = %q, err=%v", content, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(resolved.LaunchArgs[0])
		if err != nil || info.Mode()&0o111 == 0 {
			t.Fatalf("executable is not executable: info=%v err=%v", info, err)
		}
	}

	if _, err := ResolveRuntime(ResolveOptions{IndexURL: server.URL, Version: "1.2.3-rc.1", CacheDir: cacheDir}); err != nil {
		t.Fatal(err)
	}
	if indexRequests.Load() != 1 || wheelRequests.Load() != 1 {
		t.Fatalf("cache miss: index=%d wheel=%d", indexRequests.Load(), wheelRequests.Load())
	}
}

func makeTestWheel(t *testing.T, executableName string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	entries := map[string]string{
		"deepseek_harness_runtime/runtime/" + executableName: "runtime",
		"deepseek_harness_runtime/runtime/cordis.yml":        "config",
	}
	if strings.HasPrefix(executableName, "dsh-jsonrpc-agent-pkg-macos-") {
		entries["deepseek_harness_runtime/runtime/"+executableName+"-spawn-helper"] = "helper"
	}
	for name, content := range entries {
		writer, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprint(writer, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestCacheValidRequiresMacSpawnHelper(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("macOS arm64 cache layout only")
	}
	directory := t.TempDir()
	executable := filepath.Join(directory, "dsh-jsonrpc-agent-pkg-macos-arm64")
	if err := os.WriteFile(filepath.Join(directory, ".complete"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("runtime"), 0o755); err != nil {
		t.Fatal(err)
	}
	if cacheValid(directory, executable, filepath.Base(executable)) {
		t.Fatal("cache should require spawn helper")
	}
}
