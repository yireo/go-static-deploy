package deploy

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yireo/go-static-deploy/internal/magento"
)

func writeFixture(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		target := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func newFixtureDeployer(t *testing.T) (*Deployer, string) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"lib/web/lib.js":                                      "lib",
		"lib/web/shared.js":                                   "lib shared",
		"lib/web/css/docs/doc.css":                            "skipped",
		"lib/web/css/source/lib/_lib.less":                    ".lib(){}",
		"lib/web/.hidden":                                     "skipped",
		"modules/catalog/view/base/web/base.js":               "catalog base",
		"modules/catalog/view/frontend/web/js/list.js":        "catalog frontend",
		"modules/catalog/view/frontend/web/i18n/nl_NL/msg.js": "catalog nl",
		"modules/catalog/view/adminhtml/web/admin.js":         "admin only",
		"modules/catalog/view/frontend/requirejs-config.js":   "var config = {catalog: 1};",
		"modules/disabled/view/frontend/web/x.js":             "disabled",
		"themes/parent/web/shared.js":                         "parent shared",
		"themes/parent/web/css/styles.less":                   "@import 'source/lib/_lib';\n.a{}",
		"themes/parent/Vendor_Catalog/web/js/list.js":         "parent override",
		"themes/parent/requirejs-config.js":                   "var config = {parent: 1};",
		"themes/child/web/i18n/nl_NL/shared.js":               "child shared nl",
		"themes/child/web/i18n/de_DE/only-de.js":              "de",
		"themes/child/web/css/_partial.less":                  ".p{}",
		"themes/child/Vendor_Disabled/web/y.js":               "disabled module override",
	})
	modules := []magento.Component{
		{Name: "Vendor_Catalog", Path: filepath.Join(root, "modules/catalog")},
		{Name: "Vendor_Disabled", Path: filepath.Join(root, "modules/disabled")},
	}
	themes := []*magento.Theme{
		{Area: "frontend", Code: "Vendor/parent", Path: filepath.Join(root, "themes/parent")},
		{Area: "frontend", Code: "Vendor/child", Path: filepath.Join(root, "themes/child"), ParentCode: "Vendor/parent"},
	}
	env := magento.NewEnv(root, "production", modules, []string{"Vendor_Catalog"}, themes, nil)
	return New(env, nil, false, Options{Jobs: 2}, io.Discard), root
}

func TestCollectAndResolve(t *testing.T) {
	d, root := newFixtureDeployer(t)
	p := newPackage(d, "frontend", d.env.Theme("frontend", "Vendor/child"), "nl_NL")

	var keys []string
	for _, f := range p.collectFiles() {
		keys = append(keys, f.Key())
	}
	got := strings.Join(keys, ",")
	for _, want := range []string{"lib.js", "shared.js", "Vendor_Catalog::base.js", "Vendor_Catalog::js/list.js", "Vendor_Catalog::msg.js", "Vendor_Catalog::i18n/nl_NL/msg.js", "css/styles.less", "css/_partial.less"} {
		if !strings.Contains(","+got+",", ","+want+",") {
			t.Errorf("expected %s in %s", want, got)
		}
	}
	for _, unwanted := range []string{"css/docs/doc.css", ".hidden", "admin.js", "x.js", "y.js", "only-de.js"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("did not expect %s in %s", unwanted, got)
		}
	}

	resolveCases := map[string]string{
		"::shared.js":                "themes/child/web/i18n/nl_NL/shared.js",
		"::lib.js":                   "lib/web/lib.js",
		"Vendor_Catalog::js/list.js": "themes/parent/Vendor_Catalog/web/js/list.js",
		"Vendor_Catalog::msg.js":     "modules/catalog/view/frontend/web/i18n/nl_NL/msg.js",
		"Vendor_Catalog::base.js":    "modules/catalog/view/base/web/base.js",
	}
	for key, want := range resolveCases {
		module, name, _ := strings.Cut(key, "::")
		got := p.Resolve(module, name)
		if got != filepath.Join(root, want) {
			t.Errorf("Resolve(%s) = %s, want %s", key, got, want)
		}
	}

	en := newPackage(d, "frontend", d.env.Theme("frontend", "Vendor/child"), "en_US")
	if got := en.Resolve("", "shared.js"); got != filepath.Join(root, "themes/parent/web/shared.js") {
		t.Errorf("en_US shared.js resolved to %s", got)
	}
	if en.hasLocaleSpecificFiles() {
		t.Error("en_US should not have locale specific files")
	}
	if !p.hasLocaleSpecificFiles() {
		t.Error("nl_NL should have locale specific files")
	}
}

func TestPlan(t *testing.T) {
	d, _ := newFixtureDeployer(t)
	p := newPackage(d, "frontend", d.env.Theme("frontend", "Vendor/child"), "en_US")
	entries, _ := p.plan()
	kinds := map[string]entryKind{}
	for _, e := range entries {
		kinds[e.target] = e.kind
	}
	if kinds["css/styles.css"] != kindLess {
		t.Errorf("css/styles.css should be compiled from LESS: %v", kinds)
	}
	if _, ok := kinds["css/_partial.less"]; ok {
		t.Error("LESS partials must not be deployed")
	}
	if kinds["lib.js"] != kindJS {
		t.Error("lib.js should be deployed")
	}
}

func TestRequireJsConfig(t *testing.T) {
	d, _ := newFixtureDeployer(t)
	p := newPackage(d, "frontend", d.env.Theme("frontend", "Vendor/child"), "en_US")
	config, err := p.requireJsConfig()
	if err != nil {
		t.Fatal(err)
	}
	s := string(config)
	if !strings.HasPrefix(s, "(function(require){\n(function() {\nvar config = {catalog: 1};") {
		t.Errorf("unexpected start: %q", s)
	}
	if strings.Index(s, "catalog: 1") > strings.Index(s, "parent: 1") {
		t.Error("module configs must come before theme configs")
	}
	if !strings.HasSuffix(s, "require.config(config);\n})();\n\n\n\n})(require);") {
		t.Errorf("unexpected end: %q", s[len(s)-60:])
	}
}

func TestMagentoImportFiles(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"lib/web/css/source/_module.less":                     "",
		"modules/a/view/frontend/web/css/source/_module.less": "",
		"modules/b/view/base/web/css/source/_module.less":     "",
		"modules/c/view/frontend/web/css/source/_module.less": "",
		"themes/t/Vendor_A/web/css/source/_module.less":       "",
		"themes/t/Vendor_Gone/web/css/source/_module.less":    "",
		"themes/t/web/css/source/_module.less":                "",
	})
	modules := []magento.Component{
		{Name: "Vendor_C", Path: filepath.Join(root, "modules/c")},
		{Name: "Vendor_A", Path: filepath.Join(root, "modules/a")},
		{Name: "Vendor_B", Path: filepath.Join(root, "modules/b")},
	}
	themes := []*magento.Theme{{Area: "frontend", Code: "Vendor/t", Path: filepath.Join(root, "themes/t")}}
	env := magento.NewEnv(root, "production", modules, []string{"Vendor_A", "Vendor_B", "Vendor_C"}, themes, nil)
	d := New(env, nil, false, Options{Jobs: 1}, io.Discard)
	p := newPackage(d, "frontend", env.Theme("frontend", "Vendor/t"), "en_US")
	m := newLessMaterializer(p)

	content := m.processMagentoImport(asset{FilePath: "css/styles.less"}, "//@magento_import 'source/_module.less';")
	want := "@import 'source/_module.less';\n" +
		"@import 'Vendor_B::css/source/_module.less';\n" +
		"@import 'Vendor_C::css/source/_module.less';\n" +
		"@import 'Vendor_A::css/source/_module.less';\n" +
		"@import 'Vendor_Gone::css/source/_module.less';\n"
	if content != want {
		t.Errorf("got\n%s\nwant\n%s", content, want)
	}
}
