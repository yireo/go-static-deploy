package deploy

import (
	"encoding/json"
	"path/filepath"

	"github.com/yireo/go-static-deploy/internal/magento"
)

type bundleConfig struct {
	BundleSize    string   `json:"bundle_size"`
	ExcludedFiles []string `json:"excluded_files"`
	ExcludedDirs  []string `json:"excluded_dirs"`
}

// resolveThemeFile returns the first etc/view.xml in the chain of a theme (file fallback rule).
func resolveThemeFile(t *magento.Theme, rel string) string {
	for _, c := range t.Chain() {
		if f := filepath.Join(c.Path, rel); fileExists(f) {
			return f
		}
	}
	return ""
}

// viewConfig reads the JS bundling configuration of a theme from the merged view.xml files.
func (d *Deployer) viewConfig(t *magento.Theme) (*bundleConfig, error) {
	d.viewMu.Lock()
	defer d.viewMu.Unlock()
	if cfg, ok := d.viewCache[t]; ok {
		return cfg, nil
	}
	var moduleFiles []string
	for _, m := range d.env.EnabledModules {
		if f := filepath.Join(d.env.ModulePaths[m], "etc", "view.xml"); fileExists(f) {
			moduleFiles = append(moduleFiles, f)
		}
	}
	var parentFiles []string
	for cur := t; cur.Parent != nil; cur = cur.Parent {
		parentFiles = append(parentFiles, resolveThemeFile(cur.Parent, "etc/view.xml"))
	}
	resp, err := d.php.Call(map[string]any{
		"cmd":          "view-config",
		"module_files": moduleFiles,
		"theme_file":   resolveThemeFile(t, "etc/view.xml"),
		"parent_files": parentFiles,
	})
	if err != nil {
		return nil, err
	}
	var cfg bundleConfig
	if err := json.Unmarshal([]byte(resp.Content), &cfg); err != nil {
		return nil, err
	}
	d.viewCache[t] = &cfg
	return &cfg, nil
}

// bundle mirrors Magento\Deploy\Service\Bundle::deploy() using the PHP worker.
func (p *Package) bundle() error {
	cfg, err := p.d.viewConfig(p.Theme)
	if err != nil {
		return err
	}
	_, err = p.d.php.Call(map[string]any{
		"cmd":               "bundle",
		"area":              p.Area,
		"theme":             p.Theme.Code,
		"locale":            p.Locale,
		"bundle_size":       cfg.BundleSize,
		"excluded_files":    cfg.ExcludedFiles,
		"excluded_dirs":     cfg.ExcludedDirs,
		"js_minify":         p.d.minify.isEnabled("js"),
		"js_minify_exclude": p.d.minify.excludesFor("js"),
	})
	return err
}
