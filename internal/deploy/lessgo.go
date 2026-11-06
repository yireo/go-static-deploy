package deploy

import (
	"fmt"
	"os"
	"sync"

	less "github.com/toakleaf/less.go/less"
)

// LESS compilers
const (
	LessCompilerAuto = "auto"
	LessCompilerGo   = "go"
	LessCompilerPHP  = "php"
)

// ResolveLessCompiler returns the LESS compiler to use. By default (auto), wikimedia/less.php
// is used when it is installed, because its output is identical to bin/magento. When the
// installed Magento version generates source maps in developer mode, less.php is also
// preferred over an explicitly requested less.go, which cannot generate source maps in
// combination with math=always. A warning is returned for a fallback that changes the
// output, a notice for an informational change.
func ResolveLessCompiler(requested, mode string, hasLessPHP, sourceMaps bool) (compiler, warning, notice string) {
	developerSourceMaps := mode == "developer" && sourceMaps
	switch {
	case requested != LessCompilerGo && hasLessPHP:
		return LessCompilerPHP, "", ""
	case requested == LessCompilerPHP:
		return LessCompilerGo, "wikimedia/less.php is not installed; using the Go LESS compiler instead", ""
	case requested == LessCompilerAuto && developerSourceMaps:
		return LessCompilerGo, "wikimedia/less.php is not installed; LESS files are compiled with the Go LESS compiler, without source maps", ""
	case requested == LessCompilerAuto:
		return LessCompilerGo, "wikimedia/less.php is not installed; LESS files are compiled with the Go LESS compiler, so the CSS differs from bin/magento", ""
	case developerSourceMaps && hasLessPHP:
		return LessCompilerPHP, "", "developer mode: using wikimedia/less.php to generate source maps"
	case developerSourceMaps:
		return LessCompilerGo, "wikimedia/less.php is not installed; LESS files are compiled without source maps", ""
	default:
		return LessCompilerGo, "", ""
	}
}

// lessRender is less.go's render function. It is used instead of less.Compile(), which
// ignores less.Math.Always (the zero value), while Magento's LESS relies on division
// outside of parentheses (the behaviour of less.php).
var lessRender = sync.OnceValue(func() func(string, ...any) any {
	return less.Factory(nil, nil)["render"].(func(string, ...any) any)
})

// compileLessGo compiles a materialized LESS file with less.go, using the same options as
// Magento's less.php processor (no relative URL rewriting, compressed outside developer mode).
func compileLessGo(file, assetPath string, compress bool) (string, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	if phpTrim(string(data)) == "" {
		return "", fmt.Errorf("Compilation from source: LESS file is empty: %s", assetPath)
	}
	result := lessRender()(string(data), map[string]any{
		"filename":    file,
		"compress":    compress,
		"math":        less.Math.Always,
		"rewriteUrls": less.RewriteUrls.Off,
	})
	var content string
	switch r := result.(type) {
	case string:
		content = r
	case map[string]any:
		return "", fmt.Errorf("%v", r["error"])
	default:
		return "", fmt.Errorf("unexpected result of the LESS compiler: %T", result)
	}
	if phpTrim(content) == "" {
		return "", fmt.Errorf("Compilation from source: LESS file is empty: %s", assetPath)
	}
	return content, nil
}
