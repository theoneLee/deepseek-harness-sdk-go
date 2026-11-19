package deepseekharness

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// OfficeOptions configures the installed Office resource and its operation limits.
type OfficeOptions struct {
	SidecarDir           string
	TimeoutMS            int64
	MaxInputBytes        int64
	MaxOutputBytes       int64
	MaxImageResolution   int64
	MaxArchiveEntries    int64
	MaxUncompressedBytes int64
	MaxFontFiles         int64
	MaxFontFileBytes     int64
	MaxLoadedFontBytes   int64
	FontDirectories      []string
	InitialFontFamilies  []string
	FontFallbacks        [][]string
}

// OfficeConversionResult describes the saved output of a conversion or recalculation.
type OfficeConversionResult struct {
	Backend      string   `json:"backend"`
	OutputPath   string   `json:"outputPath"`
	MissingFonts []string `json:"missingFonts"`
}

type OfficeImageRectangle struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type OfficeRenderedImage struct {
	Index      int                  `json:"index"`
	Path       string               `json:"path"`
	Width      int                  `json:"width"`
	Height     int                  `json:"height"`
	Rectangle  OfficeImageRectangle `json:"rectangle"`
	ByteLength int64                `json:"byteLength"`
	Page       *int                 `json:"page"`
	Sheet      *string              `json:"sheet"`
	Range      *string              `json:"range"`
	RTL        *bool                `json:"rtl"`
}

type OfficeRenderResult struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Backend       string                `json:"backend"`
	RasterEngine  string                `json:"rasterEngine"`
	Source        string                `json:"source"`
	InputPath     string                `json:"inputPath"`
	SourceSHA256  string                `json:"sourceSha256"`
	DPI           float64               `json:"dpi"`
	PageCount     int                   `json:"pageCount"`
	Images        []OfficeRenderedImage `json:"images"`
	MissingFonts  []string              `json:"missingFonts"`
}

// OfficeRenderOptions selects pages (for example "1,3" or "all") or a worksheet and range.
type OfficeRenderOptions struct {
	OfficeOptions
	Pages        string
	Sheet        string
	Range        string
	DPI          int64
	MaxPages     int64
	MaxPixels    int64
	MaxDimension int64
}

// OfficeError reports a failed dsoffice operation.
type OfficeError struct {
	Code     string
	Message  string
	ExitCode int
}

func (e *OfficeError) Error() string { return e.Message }

// ConvertOffice exports an Office document to the format selected by outputPath.
func ConvertOffice(inputPath, outputPath, sheet string, options OfficeOptions) (OfficeConversionResult, error) {
	args := []string{"convert", "--input", inputPath, "--output", outputPath}
	args = appendOfficeArg(args, "sheet", sheet)
	var result OfficeConversionResult
	err := runOffice(args, options, &result)
	return result, err
}

// RecalculateOffice refreshes formula caches in a spreadsheet.
func RecalculateOffice(inputPath, outputPath string, options OfficeOptions) (OfficeConversionResult, error) {
	var result OfficeConversionResult
	err := runOffice([]string{"recalculate", "--input", inputPath, "--output", outputPath}, options, &result)
	return result, err
}

// RenderOfficeImages writes PNGs and a manifest for an Office document or PDF.
func RenderOfficeImages(inputPath, outputDir string, options OfficeRenderOptions) (OfficeRenderResult, error) {
	args := []string{"render", "--input", inputPath, "--output-dir", outputDir}
	args = appendOfficeArg(args, "pages", options.Pages)
	args = appendOfficeArg(args, "sheet", options.Sheet)
	args = appendOfficeArg(args, "range", options.Range)
	args = appendOfficeLimit(args, "dpi", options.DPI)
	args = appendOfficeLimit(args, "max-pages", options.MaxPages)
	args = appendOfficeLimit(args, "max-pixels", options.MaxPixels)
	args = appendOfficeLimit(args, "max-dimension", options.MaxDimension)
	var result OfficeRenderResult
	err := runOffice(args, options.OfficeOptions, &result)
	return result, err
}

func appendOfficeArg(args []string, name, value string) []string {
	if value == "" {
		return args
	}
	return append(args, "--"+name, value)
}

func appendOfficeLimit(args []string, name string, value int64) []string {
	if value == 0 {
		return args
	}
	return append(args, "--"+name, strconv.FormatInt(value, 10))
}

func runOffice(args []string, options OfficeOptions, result any) error {
	dir := options.SidecarDir
	if dir == "" {
		dir = os.Getenv("DSH_OFFICE_SIDECAR")
	}
	if !filepath.IsAbs(dir) {
		return runtimeResolveError("set OfficeOptions.SidecarDir or DSH_OFFICE_SIDECAR to the absolute path of an installed Office sidecar")
	}
	node := "node"
	if runtime.GOOS == "windows" {
		node = "node.exe"
	}
	node = filepath.Join(dir, "node", "bin", node)
	cli := filepath.Join(dir, "node_modules", "@deepseek-ai", "libreoffice-kit", "lib", "cli.js")
	for _, path := range []string{node, cli} {
		if !isFile(path) {
			return runtimeResolveError("Office sidecar is missing %s", path)
		}
	}

	for _, limit := range []struct {
		name  string
		value int64
	}{
		{"timeout-ms", options.TimeoutMS},
		{"max-input-bytes", options.MaxInputBytes},
		{"max-output-bytes", options.MaxOutputBytes},
		{"max-image-resolution", options.MaxImageResolution},
		{"max-archive-entries", options.MaxArchiveEntries},
		{"max-uncompressed-bytes", options.MaxUncompressedBytes},
		{"max-font-files", options.MaxFontFiles},
		{"max-font-file-bytes", options.MaxFontFileBytes},
		{"max-loaded-font-bytes", options.MaxLoadedFontBytes},
	} {
		args = appendOfficeLimit(args, limit.name, limit.value)
	}
	for _, path := range options.FontDirectories {
		args = append(args, "--font-directory", path)
	}
	for _, family := range options.InitialFontFamilies {
		args = append(args, "--initial-font-family", family)
	}
	if options.FontFallbacks != nil {
		groups, err := json.Marshal(options.FontFallbacks)
		if err != nil {
			return err
		}
		args = append(args, "--font-fallbacks", string(groups))
	}

	output, err := exec.Command(node, append([]string{cli}, args...)...).Output()
	if err != nil {
		if failure, ok := err.(*exec.ExitError); ok {
			var diagnostic struct {
				Code    string `json:"code"`
				Message string `json:"error"`
			}
			if json.Unmarshal(failure.Stderr, &diagnostic) != nil || diagnostic.Code == "" || diagnostic.Message == "" {
				diagnostic.Code = "failed"
				diagnostic.Message = strings.TrimSpace(string(failure.Stderr))
				if diagnostic.Message == "" {
					diagnostic.Message = "Office CLI exited without a diagnostic"
				}
			}
			return &OfficeError{Code: diagnostic.Code, Message: diagnostic.Message, ExitCode: failure.ExitCode()}
		}
		return &IOError{Err: err}
	}
	if err := json.Unmarshal(output, result); err != nil {
		return protocolError(fmt.Sprintf("Office CLI returned an invalid result: %v", err))
	}
	switch value := result.(type) {
	case *OfficeConversionResult:
		if !validOfficeBackend(value.Backend) || value.OutputPath == "" || value.MissingFonts == nil {
			return protocolError("Office CLI returned an invalid conversion result")
		}
	case *OfficeRenderResult:
		if value.SchemaVersion != 1 || !validOfficeBackend(value.Backend) ||
			(value.RasterEngine != "libreoffice" && value.RasterEngine != "pdfium") ||
			value.Source != "saved" || value.InputPath == "" || value.SourceSHA256 == "" ||
			value.DPI <= 0 || value.Images == nil || value.MissingFonts == nil {
			return protocolError("Office CLI returned an invalid render manifest")
		}
	}
	return nil
}

func validOfficeBackend(backend string) bool {
	return backend == "native" || backend == "wasm"
}
