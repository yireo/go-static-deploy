package deploy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yireo/go-static-deploy/internal/magento"
)

func TestMinifyHTMLTemplate(t *testing.T) {
	src := `<?php
// comment
/** @var $block \Magento\Framework\View\Element\Template */
$x = <<<HTML
  keep   this
HTML;
?>
<div   class="a">
    <span> text </span> <b>bold</b>
    <a href="http://example.com">link</a>
    <pre>
  keep   spaces
    </pre>
    <script>
        var a = 1; // js comment
    </script>
    <?= $block->escapeHtml(__("Hi")) ?>
</div>
`
	// Output of Magento\Framework\View\Template\Html\Minifier::minify()
	want := `<?php /** @var $block \Magento\Framework\View\Element\Template */ $x = <<<HTML
  keep   this
HTML; ?> <div class="a"><span> text</span> <b>bold</b> <a href="http://example.com">link</a> <pre>
  keep   spaces</pre><script>
        var a = 1;</script><?= $block->escapeHtml(__("Hi")) ?></div>`
	got, err := MinifyHTMLTemplate([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}

	// Non-UTF-8 bytes must survive unchanged
	got, _ = MinifyHTMLTemplate([]byte("<p>\xa0\x85\xff</p>"))
	if string(got) != "<p>\xa0\x85\xff</p>" {
		t.Errorf("binary content changed: %q", got)
	}
}

func TestShouldMinifyHTML(t *testing.T) {
	on := magento.NewConfig()
	on.Set("dev/template/minify_html", "1")
	off := magento.NewConfig()
	tests := []struct {
		setting, mode string
		cfg           *magento.Config
		withDB, want  bool
	}{
		{HTMLMinifyAuto, "production", on, true, true},
		{HTMLMinifyAuto, "production", off, true, false},
		{HTMLMinifyAuto, "production", off, false, true},
		{HTMLMinifyAuto, "developer", on, true, false},
		{HTMLMinifyAuto, "default", on, false, false},
		{HTMLMinifyOn, "developer", off, true, true},
		{HTMLMinifyOff, "production", on, true, false},
	}
	for _, tt := range tests {
		if got := ShouldMinifyHTML(tt.setting, tt.mode, tt.cfg, tt.withDB); got != tt.want {
			t.Errorf("ShouldMinifyHTML(%s, %s, withDB=%v) = %v, want %v", tt.setting, tt.mode, tt.withDB, got, tt.want)
		}
	}
}

func TestMinifyTemplatesGo(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(content), 0o644)
	}
	write("vendor/magento/module-theme/view/base/templates/root.phtml", "<div>\n  <p>x</p>\n</div>\n")
	write("vendor/magento/module-theme/view/base/web/ignored.phtml", "x")
	write("app/design/frontend/Foo/bar/Magento_Theme/templates/html/header.phtml", "<header>\n</header>")
	write("app/design/frontend/Foo/bar/templates/ignored.phtml", "x")
	write("var/view_preprocessed/pub/static/vendor/stale.phtml", "stale")

	env := &magento.Env{
		Root:              root,
		RegisteredModules: []magento.Component{{Name: "Magento_Theme", Path: filepath.Join(root, "vendor/magento/module-theme")}},
		Themes:            map[string]*magento.Theme{"frontend/Foo/bar": {Path: filepath.Join(root, "app/design/frontend/Foo/bar")}},
	}
	d := &Deployer{env: env, opts: Options{Jobs: 2, HTMLMinifier: HTMLMinifierGo}}
	count, err := d.MinifyTemplates()
	if err != nil || count != 2 {
		t.Fatalf("count %d, err %v", count, err)
	}
	tmp := filepath.Join(root, "var/view_preprocessed/pub/static")
	if data, _ := os.ReadFile(filepath.Join(tmp, "vendor/magento/module-theme/view/base/templates/root.phtml")); string(data) != "<div><p>x</p></div>" {
		t.Errorf("root.phtml: %q", data)
	}
	if !fileExists(filepath.Join(tmp, "app/design/frontend/Foo/bar/Magento_Theme/templates/html/header.phtml")) {
		t.Error("theme template not minified")
	}
	if fileExists(filepath.Join(tmp, "vendor/stale.phtml")) {
		t.Error("stale template not removed")
	}
}
