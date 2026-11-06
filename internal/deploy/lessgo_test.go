package deploy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func writeLessFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := writeFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestCompileLessGo(t *testing.T) {
	dir := writeLessFiles(t, map[string]string{
		"css/styles.less": "@import 'source/_lib.less';\n" +
			"@w: 10px;\n" +
			".a { width: @w / 2; height: (@w / 2); margin: @gutter * 2; }\n" +
			".b { background: url('../images/a.png'); icon: url('Magento_Theme::x.svg'); }\n" +
			".c { .d { color: darken(#ffffff, 10%); } content: ~\"'x'\"; }\n",
		"css/source/_lib.less": "@gutter: 4px;\n.lib { background: url('img/lib.png'); }\n",
	})
	got, err := compileLessGo(filepath.Join(dir, "css/styles.less"), "css/styles.css", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"width:5px", "height:5px", "margin:8px",
		"url('../images/a.png')", "url('Magento_Theme::x.svg')", "url('img/lib.png')",
		".c .d{color:#e6e6e6}", "content:'x'",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("compiled CSS does not contain %q:\n%s", want, got)
		}
	}

	expanded, err := compileLessGo(filepath.Join(dir, "css/styles.less"), "css/styles.css", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(expanded, "\n") {
		t.Errorf("uncompressed CSS expected:\n%s", expanded)
	}
}

func TestCompileLessGoErrors(t *testing.T) {
	dir := writeLessFiles(t, map[string]string{
		"empty.less":    " \n\t",
		"nooutput.less": "@a: 1px;\n.mixin() { a: b; }\n",
		"broken.less":   ".a {",
	})
	for name, want := range map[string]string{
		"empty.less":    "LESS file is empty: x.css",
		"nooutput.less": "LESS file is empty: x.css",
		"broken.less":   "",
	} {
		_, err := compileLessGo(filepath.Join(dir, name), "x.css", true)
		if err == nil {
			t.Errorf("%s: expected an error", name)
			continue
		}
		if want != "" && !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got error %q, want %q", name, err, want)
		}
	}
}

func TestCompileLessGoConcurrent(t *testing.T) {
	files := map[string]string{"source/_lib.less": "@c: #333;\n.m(@x) { width: @x / 2; }\n"}
	for i := range 8 {
		files[fmt.Sprintf("s%d.less", i)] = fmt.Sprintf("@import 'source/_lib';\n.a%d { color: @c; .m(%dpx); }\n", i, (i+1)*10)
	}
	dir := writeLessFiles(t, files)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for range 5 {
				got, err := compileLessGo(filepath.Join(dir, fmt.Sprintf("s%d.less", i)), "x.css", true)
				if err != nil {
					t.Error(err)
					return
				}
				if want := fmt.Sprintf(".a%d{color:#333;width:%dpx}", i, (i+1)*5); got != want {
					t.Errorf("got %q, want %q", got, want)
					return
				}
			}
		})
	}
	wg.Wait()
}

func TestResolveLessCompiler(t *testing.T) {
	cases := []struct {
		requested, mode string
		hasLessPHP      bool
		sourceMaps      bool
		want            string
		warning         bool
	}{
		{LessCompilerAuto, "production", true, true, LessCompilerPHP, false},
		{LessCompilerAuto, "production", false, true, LessCompilerGo, true},
		{LessCompilerAuto, "developer", true, true, LessCompilerPHP, false},
		{LessCompilerAuto, "developer", false, true, LessCompilerGo, true},
		{LessCompilerGo, "production", true, true, LessCompilerGo, false},
		{LessCompilerGo, "production", false, true, LessCompilerGo, false},
		{LessCompilerPHP, "production", true, true, LessCompilerPHP, false},
		{LessCompilerPHP, "production", false, true, LessCompilerGo, true},
		{LessCompilerGo, "developer", true, true, LessCompilerPHP, false},
		{LessCompilerGo, "developer", false, true, LessCompilerGo, true},
		{LessCompilerGo, "developer", true, false, LessCompilerGo, false},
		{LessCompilerGo, "developer", false, false, LessCompilerGo, false},
		{LessCompilerPHP, "developer", true, true, LessCompilerPHP, false},
	}
	for _, c := range cases {
		got, warning, _ := ResolveLessCompiler(c.requested, c.mode, c.hasLessPHP, c.sourceMaps)
		if got != c.want || (warning != "") != c.warning {
			t.Errorf("ResolveLessCompiler(%q, %q, %v, %v) = %q, %q", c.requested, c.mode, c.hasLessPHP, c.sourceMaps, got, warning)
		}
	}
}

// TestCompileLessGoMatchesLessPHP compares less.go with wikimedia/less.php for the root LESS
// files of a Magento installation that were materialized in var/view_preprocessed by a
// previous deployment, after minifying both. Set GSD_TEST_MAGENTO_ROOT to run it.
func TestCompileLessGoMatchesLessPHP(t *testing.T) {
	root := os.Getenv("GSD_TEST_MAGENTO_ROOT")
	if root == "" {
		t.Skip("GSD_TEST_MAGENTO_ROOT not set")
	}
	files, _ := filepath.Glob(filepath.Join(root, "var/view_preprocessed/pub/static/frontend/*/*/*/css/*.less"))
	if len(files) == 0 {
		t.Skip("no materialized LESS files found")
	}
	mf := newMinification(false, false, nil, nil)
	for _, file := range files {
		if strings.HasPrefix(filepath.Base(file), "_") {
			continue
		}
		t.Run(strings.TrimPrefix(file, root), func(t *testing.T) {
			want := runPHP(t, `require $argv[1] . '/vendor/autoload.php';
$parser = new \Less_Parser(['relativeUrls' => false, 'compress' => true]);
$parser->parseFile($argv[2], '');
echo $parser->getCss();`, nil, root, file)
			got, err := compileLessGo(file, "x.css", true)
			if err != nil {
				t.Fatal(err)
			}
			g, w := string(mf.minifyCSS([]byte(got))), string(mf.minifyCSS([]byte(want)))
			if g != w {
				i := 0
				for i < len(g) && i < len(w) && g[i] == w[i] {
					i++
				}
				t.Errorf("output differs at byte %d:\n go: %.200q\nphp: %.200q", i, g[max(0, i-80):], w[max(0, i-80):])
			}
		})
	}
}
