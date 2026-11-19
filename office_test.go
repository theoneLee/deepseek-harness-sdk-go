package deepseekharness

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestOfficeConversionCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a shell script")
	}
	dir := t.TempDir()
	node := filepath.Join(dir, "node", "bin", "node")
	cli := filepath.Join(dir, "node_modules", "@deepseek-ai", "libreoffice-kit", "lib", "cli.js")
	for _, path := range []string{node, cli} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/bin/sh
if [ "$*" != "$1 convert --input input.xlsx --output output.csv --sheet Sheet One --timeout-ms 1000" ]; then
	printf '%s' '{"code":"invalid-arguments","error":"unexpected option values"}' >&2
	exit 2
fi
printf '%s' '{"backend":"native","outputPath":"output.csv","missingFonts":[]}'
`
	if err := os.WriteFile(node, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cli, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	options := OfficeOptions{SidecarDir: dir, TimeoutMS: 1000}
	result, err := ConvertOffice("input.xlsx", "output.csv", "Sheet One", options)
	if err != nil || result.OutputPath != "output.csv" {
		t.Fatalf("conversion = %#v, %v", result, err)
	}
	_, err = ConvertOffice("input.xlsx", "output.csv", "Other sheet", options)
	var failure *OfficeError
	if !errors.As(err, &failure) || failure.Code != "invalid-arguments" || failure.ExitCode != 2 {
		t.Fatalf("expected typed CLI error, got %v", err)
	}
}
