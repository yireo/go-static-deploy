package deploy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAreaSRIHashes(t *testing.T) {
	d, root := newFixtureDeployer(t)
	csp := filepath.Join(root, "modules/csp")
	writeFixture(t, root, map[string]string{
		"modules/csp/Model/SubresourceIntegrity/Storage/File.php": "<?php",
		"pub/static/frontend/sri-hashes.json":                     `{"frontend\/Other\/theme\/en_US\/a.js":"sha256-other"}`,
		"lib/web/both.js":                                         "both",
		"lib/web/both.min.js":                                     "both minified",
	})
	d.env.ModulePaths["Magento_Csp"] = csp
	d.minify = newMinification(true, false, nil, nil)

	p := newPackage(d, "frontend", d.env.Theme("frontend", "Vendor/child"), "en_US")
	outDir, _ := d.packageDirs(p)
	entries, _ := p.plan()
	deployed := map[string]string{
		"requirejs-config.min.js":       "config",
		"requirejs-min-resolver.min.js": "resolver",
		"js/bundle/bundle0.min.js":      "bundle",
	}
	for _, e := range entries {
		if e.kind == kindJS {
			deployed[e.target] = "minified " + e.source
		}
	}
	for rel, content := range deployed {
		if err := writeFile(filepath.Join(outDir, rel), []byte(content)); err != nil {
			t.Fatal(err)
		}
	}

	if err := p.writeSRIHashes(outDir, entries); err != nil {
		t.Fatal(err)
	}
	if err := d.WriteSRIHashes(); err != nil {
		t.Fatal(err)
	}
	if fileExists(filepath.Join(outDir, sriFile)) {
		t.Error("sri-hashes.json should not be written in the package")
	}
	data, err := os.ReadFile(filepath.Join(root, "pub/static/frontend", sriFile))
	if err != nil {
		t.Fatal(err)
	}
	var hashes map[string]string
	if err := json.Unmarshal(data, &hashes); err != nil {
		t.Fatal(err)
	}

	pkg := "frontend/Vendor/child/en_US/"
	sourceHash, _ := fileHash(filepath.Join(root, "themes/parent/Vendor_Catalog/web/js/list.js"))
	bundleHash, _ := fileHash(filepath.Join(outDir, "js/bundle/bundle0.min.js"))
	bothHash, _ := fileHash(filepath.Join(root, "lib/web/both.js"))
	bothMinHash, _ := fileHash(filepath.Join(outDir, "both.min.js"))
	want := map[string]string{
		pkg + "both.js":                   bothHash,
		pkg + "both.min.js":               bothMinHash,
		pkg + "Vendor_Catalog/js/list.js": sourceHash,
		pkg + "js/bundle/bundle0.min.js":  bundleHash,
		"frontend/Other/theme/en_US/a.js": "sha256-other",
	}
	for k, v := range want {
		if hashes[k] != v {
			t.Errorf("hash of %s = %q, want %q", k, hashes[k], v)
		}
	}
	for _, k := range []string{pkg + "Vendor_Catalog/js/list.min.js", pkg + "requirejs-min-resolver.min.js"} {
		if _, ok := hashes[k]; ok {
			t.Errorf("unexpected entry %s", k)
		}
	}
	if _, ok := hashes[pkg+"requirejs-config.min.js"]; !ok {
		t.Error("missing requirejs-config.min.js")
	}
}

func TestPHPJSONObject(t *testing.T) {
	got := string(phpJSONObject(map[string]string{
		"frontend/a/é.js": "sha256-a/b+c=",
		"b\"\\":           "\n😀",
	}))
	want := `{"b\"\\":"\n\ud83d\ude00","frontend\/a\/\u00e9.js":"sha256-a\/b+c="}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestMinifiedName(t *testing.T) {
	mf := newMinification(true, false, []string{"/hugerte/"}, nil)
	cases := map[string]string{
		"frontend/A/b/en_US/js/x.js":           "frontend/A/b/en_US/js/x.min.js",
		"frontend/A/b/en_US/js/x.min.js":       "frontend/A/b/en_US/js/x.min.js",
		"frontend/A/b/en_US/hugerte/plugin.js": "frontend/A/b/en_US/hugerte/plugin.js",
		"frontend/A/b/en_US/css/styles.css":    "frontend/A/b/en_US/css/styles.css",
	}
	for in, want := range cases {
		if got := mf.addMinifiedSign(in); got != want {
			t.Errorf("addMinifiedSign(%s) = %s, want %s", in, got, want)
		}
	}
}
