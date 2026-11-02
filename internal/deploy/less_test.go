package deploy

import (
	"encoding/json"
	"os/exec"
	"testing"
)

func TestNormalizePath(t *testing.T) {
	cases := map[string]string{
		"a/b/../c":        "a/c",
		"./a/./b":         "a/b",
		"../a":            "../a",
		"a/../../b":       "../b",
		"../../x/../y.js": "../../y.js",
	}
	for in, want := range cases {
		if got := normalizePath(in); got != want {
			t.Errorf("normalizePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOffsetPath(t *testing.T) {
	cases := []struct{ related, path, want string }{
		{"frontend/Magento/luma/en_US/Magento_Catalog/css/source/_module.less", "frontend/Magento/luma/en_US/css/styles-m.less", "../Magento_Catalog/css/source"},
		{"frontend/Magento/luma/en_US/css/source/_x.less", "frontend/Magento/luma/en_US/css/styles-m.less", "source"},
		{"frontend/Magento/luma/en_US/x.less", "frontend/Magento/luma/en_US/y.less", "."},
	}
	for _, c := range cases {
		if got := offsetPath(c.related, c.path); got != c.want {
			t.Errorf("offsetPath(%q, %q) = %q, want %q", c.related, c.path, got, c.want)
		}
	}
}

func TestFixFileExtension(t *testing.T) {
	if got := fixFileExtension("source/_lib", "less"); got != "source/_lib.less" {
		t.Errorf("got %q", got)
	}
	if got := fixFileExtension("source/_lib.css", "less"); got != "source/_lib.css" {
		t.Errorf("got %q", got)
	}
	if got := fixFileExtension("dir.v1/file", "less"); got != "dir.v1/file.less" {
		t.Errorf("got %q", got)
	}
}

func TestOrderedFilesReplace(t *testing.T) {
	o := &orderedFiles{}
	o.add("base|module:A|file:_module.less", importFile{module: "A"})
	o.add("|module:B|file:_module.less", importFile{module: "B"})
	o.add("|module:C|file:_module.less", importFile{module: "C"})
	// A theme override moves the module to the end of the list
	o.replace("|module:A|file:_module.less", importFile{module: "A"})
	var got []string
	for _, f := range o.all() {
		got = append(got, f.module)
	}
	want := []string{"B", "C", "A"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("got %v, want %v", got, want)
	}
}

var lessSamples = []string{
	"// comment\n@import 'a';\n",
	"@import 'source/_lib';  // trailing\n/* block */\n@import (reference) 'x.less';\n",
	"  /* multi\nline */\n.a { color: red; } /* not removed */\n",
	"/* a */ .b {}\n/* c */\n",
	"@import url('foo.css');\n@import (css) url(\"bar.css\");\n@import 'Magento_Catalog::css/source/_module.less';\n",
	"\n\n   // indented\n\t/* tab */\n@import \"double\";\n",
	"/* x */*/\n.c{}\n/* y */\n",
	"@import 'a', 'b';\n@import'nospace';\n",
}

func runPHP(t *testing.T, code string, input any) string {
	t.Helper()
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("php not available")
	}
	data, _ := json.Marshal(input)
	cmd := exec.Command(php, "-r", code)
	stdin, _ := cmd.StdinPipe()
	go func() {
		stdin.Write(data)
		stdin.Close()
	}()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("php failed: %v", err)
	}
	return string(out)
}

// TestRemoveCommentsMatchesPHP compares removeComments() with the PCRE expression of Magento.
func TestRemoveCommentsMatchesPHP(t *testing.T) {
	out := runPHP(t, `$in = json_decode(stream_get_contents(STDIN), true); $out = [];
foreach ($in as $s) { $out[] = preg_replace("#(^\s*//.*$)|((^\s*/\*(?s).*?(\*/)(?!\*/))$)#m", '', $s); }
echo json_encode($out);`, lessSamples)
	var want []string
	if err := json.Unmarshal([]byte(out), &want); err != nil {
		t.Fatal(err)
	}
	for i, sample := range lessSamples {
		if got := removeComments(sample); got != want[i] {
			t.Errorf("sample %d: got %q, want %q", i, got, want[i])
		}
	}
}

// TestImportRegexMatchesPHP compares the @import expression with Magento's PCRE expression.
func TestImportRegexMatchesPHP(t *testing.T) {
	out := runPHP(t, `$in = json_decode(stream_get_contents(STDIN), true); $out = [];
$p = '#@import[\s]*(?P<start>[\(\),\w\s]*?[\'\"][\s]*)(?P<path>[^\)\'\"]*?)(?P<end>[\s]*[\'\"][\s\w]*[\)]?)[\s]*;#';
foreach ($in as $s) { preg_match_all($p, $s, $m, PREG_SET_ORDER); $r = [];
  foreach ($m as $x) { $r[] = [$x[0], $x['start'], $x['path'], $x['end']]; } $out[] = $r; }
echo json_encode($out);`, lessSamples)
	var want [][][]string
	if err := json.Unmarshal([]byte(out), &want); err != nil {
		t.Fatal(err)
	}
	for i, sample := range lessSamples {
		got := importRegex.FindAllStringSubmatch(sample, -1)
		if len(got) != len(want[i]) {
			t.Errorf("sample %d: got %d matches, want %d", i, len(got), len(want[i]))
			continue
		}
		for j := range got {
			for k := 0; k < 4; k++ {
				if got[j][k] != want[i][j][k] {
					t.Errorf("sample %d match %d group %d: got %q, want %q", i, j, k, got[j][k], want[i][j][k])
				}
			}
		}
	}
}
