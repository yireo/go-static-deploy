package deploy

import (
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestJShrinkMinify(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"keeps require argument", "define(function (require) {\n    var r = require('Magento_Ui/js/lib/knockout/template/renderer');\n    return r;\n});",
			"define(function(require){var r=require('Magento_Ui/js/lib/knockout/template/renderer');return r;});"},
		{"keeps unused arguments and $", "function template(tmpl, $, $$) {\n    return eval('`' + tmpl + '`');\n}",
			"function template(tmpl,$,$$){return eval('`'+tmpl+'`');}"},
		{"removes comments", "// line\nvar a = 1; /* block */ var b = 2;", "var a=1;var b=2;"},
		{"keeps flagged comments", "/*! license */\nvar a = 1;", "/*! license */\nvar a=1;"},
		{"keeps newline for ASI", "a = b\n(c)", "a=b\n(c)"},
		{"keeps newline after closing brace", "}\nfoo()", "}\nfoo()"},
		{"keeps string contents", `var s = "a  //  b";`, `var s="a  //  b";`},
		{"keeps template literal newlines", "var s = `a\n  b`;", "var s=`a\n  b`;"},
		{"keeps regex", "var r = /a\\/ +[/]/g;", "var r=/a\\/ +[/]/g;"},
		{"regex after return", "return /x y/.test(s)", "return/x y/.test(s)"},
		{"locks plus plus", "a = 'x' + ++i;", "a='x'+ ++i;"},
		{"keeps word spacing", "return typeof x", "return typeof x"},
	}
	for _, c := range cases {
		got, err := jshrinkMinify(c.in)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
		} else if got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestJShrinkErrors(t *testing.T) {
	for _, in := range []string{"var s = 'a\nb';", "/* open", "var r = (/abc\n);"} {
		if _, err := jshrinkMinify(in); err == nil {
			t.Errorf("expected error for %q", in)
		}
	}
	mf := newMinification(true, false, nil, nil)
	if got := string(mf.minifyJS([]byte("/* open"))); got != "/* open" {
		t.Errorf("failed minification should keep the content, got %q", got)
	}
}

// TestJShrinkMatchesPHP compares the port with JShrink itself for all JavaScript files of a Magento root.
// Set GSD_TEST_MAGENTO_ROOT to run it.
func TestJShrinkMatchesPHP(t *testing.T) {
	root := os.Getenv("GSD_TEST_MAGENTO_ROOT")
	if root == "" {
		t.Skip("GSD_TEST_MAGENTO_ROOT not set")
	}
	if _, err := exec.LookPath("php"); err != nil {
		t.Skip("php not found")
	}
	var files []string
	for _, dir := range []string{"lib/web", "vendor", "app/code", "app/design"} {
		_ = filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(p, ".js") && !strings.Contains(p, "/node_modules/") &&
				!strings.Contains(p, "/Test/") && !strings.Contains(p, "/tests/") {
				files = append(files, p)
			}
			return nil
		})
	}
	if len(files) == 0 {
		t.Skip("no JavaScript files found")
	}
	list := filepath.Join(t.TempDir(), "files.json")
	data, _ := json.Marshal(files)
	if err := os.WriteFile(list, data, 0o644); err != nil {
		t.Fatal(err)
	}
	script := `require $argv[1] . '/vendor/tedivm/jshrink/src/JShrink/Minifier.php';
$out = [];
foreach (json_decode(file_get_contents($argv[2]), true) as $f) {
    try { $out[$f] = base64_encode(\JShrink\Minifier::minify(file_get_contents($f))); }
    catch (\Throwable $e) { $out[$f] = null; }
}
echo json_encode($out);`
	cmd := exec.Command("php", "-d", "memory_limit=-1", "-d", "error_reporting=0", "-r", script, root, list)
	raw, err := cmd.Output()
	if err != nil {
		t.Fatalf("php: %v", err)
	}
	var expected map[string]*string
	if err := json.Unmarshal(raw, &expected); err != nil {
		t.Fatal(err)
	}
	failures, compared := 0, 0
	for _, f := range files {
		src, _ := os.ReadFile(f)
		got, gotErr := jshrinkMinify(string(src))
		want, ok := expected[f]
		if !ok {
			continue
		}
		compared++
		if want == nil {
			if gotErr == nil {
				t.Errorf("%s: JShrink failed but the port did not", f)
				failures++
			}
		} else if gotErr != nil {
			t.Errorf("%s: port failed: %v", f, gotErr)
			failures++
		} else if wantStr := mustBase64(t, *want); got != wantStr {
			i := 0
			for i < len(got) && i < len(wantStr) && got[i] == wantStr[i] {
				i++
			}
			t.Errorf("%s: differs at byte %d:\n got %q\nwant %q", f, i, snippet(got, i), snippet(wantStr, i))
			failures++
		}
		if failures >= 20 {
			t.Fatal("too many differences")
		}
	}
	if compared == 0 {
		t.Fatal("php returned no results")
	}
	t.Logf("compared %d of %d files", compared, len(files))
}

func mustBase64(t *testing.T, s string) string {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func snippet(s string, i int) string {
	return s[max(0, i-40):min(len(s), i+40)]
}
