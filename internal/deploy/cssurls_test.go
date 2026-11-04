package deploy

import (
	"encoding/json"
	"testing"
)

var cssURLSamples = []string{
	`.a{background:url(../images/a.png)}`,
	`.a{background:url('../images/a.png') no-repeat}.b{background:url("b.png")}`,
	`.a{background:url( "images/a.png" )}`,
	`.a{background:url(/abs.png);b:url(http://x/y.png);c:url(https://x/y.png);d:url(data:image/png;base64,AAA=)}`,
	`.a{b:url('/abs.png');c:url("data:x")}`,
	`.a{b:url( "/abs.png")}`,
	`@font-face{src:url('../fonts/a.eot?#iefix') format('embedded-opentype'),url('../fonts/a.woff2') format('woff2'),url("a.svg#icons") format('svg')}`,
	`.a{b:url(a.png?v=1);c:url(a.png#x)}`,
	`.a{b:url('{{base_url_path}}x.png');c:url({{BASE_URL_PATH}}y.png);d:url({{media}}z.png)}`,
	`.a{b:url('Magento_Theme::images/a.png');c:url(Magento_Theme::b.png);d:url('Magento_Catalog::images/../x.svg#a')}`,
	"x { b: url(\n'a.png'); c: url('b.png'\n) }",
	"x { b: url('a.png\n') ; c: url (c.png) ; d: url ( 'd.png' ) }",
	`url(a.png) url(a.png) url('a.png') url(nothing`,
	`.a{b:URL(a.png);c:url()}`,
	`.a{b:url(a.png?x=(1));c:url('a b.png')}`,
}

// TestExtractRelativeUrlsMatchesPHP compares extractRelativeUrls() with
// CssResolver::extractRelativeUrls().
func TestExtractRelativeUrlsMatchesPHP(t *testing.T) {
	out := runPHP(t, `$in = json_decode(stream_get_contents(STDIN), true); $out = [];
$p = '#url\s*\(\s*(?(?=\'|").)(?!http\://|https\://|/|data\:)(.+?)(?:[\#\?].*?|[\'"])?\s*\)#';
foreach ($in as $s) { preg_match_all($p, $s, $m); $r = [];
  if (!empty($m[0]) && !empty($m[1])) { foreach (array_combine($m[0], $m[1]) as $k => $v) { $r[] = [(string)$k, $v]; } }
  $out[] = $r; }
echo json_encode($out);`, cssURLSamples)
	var want [][][2]string
	if err := json.Unmarshal([]byte(out), &want); err != nil {
		t.Fatal(err)
	}
	for i, sample := range cssURLSamples {
		patterns, paths := extractRelativeUrls(sample)
		if len(patterns) != len(want[i]) {
			t.Errorf("sample %d: got %d matches %q, want %d %q", i, len(patterns), patterns, len(want[i]), want[i])
			continue
		}
		for j, p := range patterns {
			if p != want[i][j][0] || paths[p] != want[i][j][1] {
				t.Errorf("sample %d match %d: got [%q %q], want %q", i, j, p, paths[p], want[i][j])
			}
		}
	}
}

// TestReplaceCSSVariablesMatchesPHP compares replaceCSSVariables() with
// CssResolver::replaceRelativeUrls() and Magento's variable notation callback.
func TestReplaceCSSVariablesMatchesPHP(t *testing.T) {
	out := runPHP(t, `$in = json_decode(stream_get_contents(STDIN), true); $out = [];
$area = 'frontend'; $themePath = 'Magento/luma';
$cb = function ($path) use ($area, $themePath) {
    if (!preg_match_all('/{{([_a-z]*)}}/si', (string)$path, $matches, PREG_SET_ORDER)) { return $path; }
    $replacements = [];
    foreach ($matches as $match) {
        if (!isset($replacements[$match[0]])) {
            $replacements[$match[0]] = $match[1] === 'base_url_path'
                ? '{{base_url_path}}' . $area . ($themePath ? '/' . $themePath . '/' : '') . '{{locale}}' : '';
        }
    }
    return str_replace(array_keys($replacements), $replacements, (string)$path);
};
$p = '#url\s*\(\s*(?(?=\'|").)(?!http\://|https\://|/|data\:)(.+?)(?:[\#\?].*?|[\'"])?\s*\)#';
foreach ($in as $css) {
    preg_match_all($p, $css, $m);
    $patterns = (!empty($m[0]) && !empty($m[1])) ? array_combine($m[0], $m[1]) : [];
    $replace = [];
    foreach ($patterns as $pattern => $path) {
        if (!isset($replace[$pattern])) { $replace[$pattern] = str_replace($path, $cb($path), $pattern); }
    }
    if ($replace) { $css = str_replace(array_keys($replace), array_values($replace), $css); }
    $out[] = $css;
}
echo json_encode($out);`, cssURLSamples)
	var want []string
	if err := json.Unmarshal([]byte(out), &want); err != nil {
		t.Fatal(err)
	}
	for i, sample := range cssURLSamples {
		if got := replaceCSSVariables(sample, "frontend", "Magento/luma"); got != want[i] {
			t.Errorf("sample %d: got %q, want %q", i, got, want[i])
		}
	}
}

func TestProcessCSSModuleNotation(t *testing.T) {
	cases := []struct{ asset, in, want string }{
		{"css/styles-m.css", `.a{b:url('Magento_Theme::images/a.png')}`, `.a{b:url('../Magento_Theme/images/a.png')}`},
		{"css/styles-m.css", `.a{b:url(Magento_Theme::b.png)}`, `.a{b:url(../Magento_Theme/b.png)}`},
		{"Magento_Catalog/css/x.css", `.a{b:url("Magento_Catalog::images/../x.svg#a")}`, `.a{b:url("../x.svg#a")}`},
		{"css/styles-m.css", `.a{b:url(images/a.png)}`, `.a{b:url(images/a.png)}`},
		{"css/styles-m.css", `.a{b:url('{{base_url_path}}x.png')}`, `.a{b:url('{{base_url_path}}frontend/Magento/luma/{{locale}}x.png')}`},
	}
	for _, c := range cases {
		if got := processCSS(c.in, "frontend", "Magento/luma", "frontend/Magento/luma/en_US", c.asset); got != c.want {
			t.Errorf("processCSS(%q, %q) = %q, want %q", c.in, c.asset, got, c.want)
		}
	}
}
