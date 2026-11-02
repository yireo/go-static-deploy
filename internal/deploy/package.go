package deploy

import (
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/yireo/go-static-deploy/internal/magento"
)

var (
	themeModuleDirRegex = regexp.MustCompile(`(?i)^[a-z\d]+_[a-z_\d]+$`)
	localeDirRegex      = regexp.MustCompile(`(?i)^[a-z_]+$`)
)

// Package is a combination of area, theme and locale, deployed to pub/static/<area>/<theme>/<locale>.
type Package struct {
	Area   string
	Theme  *magento.Theme
	Locale string

	d *Deployer

	modMu     sync.Mutex
	modDirs   map[string][]string
	themeMods map[*magento.Theme][]string
}

// Path returns area/Vendor/theme/locale.
func (p *Package) Path() string { return p.Area + "/" + p.Theme.Code + "/" + p.Locale }

func newPackage(d *Deployer, area string, theme *magento.Theme, locale string) *Package {
	return &Package{
		Area: area, Theme: theme, Locale: locale, d: d,
		modDirs:   map[string][]string{},
		themeMods: map[*magento.Theme][]string{},
	}
}

func (p *Package) libDir() string { return filepath.Join(p.d.env.Root, "lib", "web") }

// nonModuleDirs returns the fallback directories for files without module, highest priority first.
// Same as the non-modular part of Magento's static view file fallback rule.
func (p *Package) nonModuleDirs() []string {
	var dirs []string
	for _, t := range p.Theme.Chain() {
		dirs = append(dirs, filepath.Join(t.Path, "web", "i18n", p.Locale), filepath.Join(t.Path, "web"))
	}
	return append(dirs, p.libDir())
}

// moduleDirs returns the fallback directories for a module, highest priority first.
func (p *Package) moduleDirs(module string) []string {
	p.modMu.Lock()
	defer p.modMu.Unlock()
	if dirs, ok := p.modDirs[module]; ok {
		return dirs
	}
	var dirs []string
	for _, t := range p.Theme.Chain() {
		dirs = append(dirs, filepath.Join(t.Path, module, "web", "i18n", p.Locale), filepath.Join(t.Path, module, "web"))
	}
	if modPath, ok := p.d.env.ModulePaths[module]; ok {
		dirs = append(dirs,
			filepath.Join(modPath, "view", p.Area, "web", "i18n", p.Locale),
			filepath.Join(modPath, "view", "base", "web", "i18n", p.Locale),
			filepath.Join(modPath, "view", p.Area, "web"),
			filepath.Join(modPath, "view", "base", "web"),
		)
	}
	p.modDirs[module] = dirs
	return dirs
}

// Resolve finds the source file of a static view file using Magento's fallback rules.
func (p *Package) Resolve(module, rel string) string {
	if strings.HasPrefix(rel, "/") {
		return ""
	}
	var dirs []string
	if module == "" {
		dirs = p.nonModuleDirs()
	} else {
		dirs = p.moduleDirs(module)
	}
	for _, dir := range dirs {
		if p.d.index.Has(dir, rel) {
			return filepath.Join(dir, filepath.FromSlash(rel))
		}
	}
	return ""
}

// ResolveMin resolves a file honouring minification: when minification applies, the
// .min variant is looked up first (Magento's Minification fallback resolver).
func (p *Package) ResolveMin(module, rel string) (string, bool) {
	// Like Magento's Minification fallback resolver, the exclude list is matched against
	// the file path without area, theme, locale and module.
	if p.d.minify.addMinifiedSign(rel) != rel {
		ext := path.Ext(rel)
		minRel := strings.TrimSuffix(rel, ext) + ".min" + ext
		if src := p.Resolve(module, minRel); src != "" {
			return src, true
		}
	}
	src := p.Resolve(module, rel)
	return src, src != "" && isMinifiedFilename(src)
}

// themeModules returns the module override directories (Vendor_Module) of a theme.
func (p *Package) themeModules(t *magento.Theme) []string {
	p.modMu.Lock()
	defer p.modMu.Unlock()
	if mods, ok := p.themeMods[t]; ok {
		return mods
	}
	var mods []string
	for _, sub := range p.d.index.Subdirs(t.Path) {
		if themeModuleDirRegex.MatchString(sub) {
			mods = append(mods, sub)
		}
	}
	p.themeMods[t] = mods
	return mods
}

// FileID identifies a static file in a package.
type FileID struct {
	Module string
	Name   string // source file name relative to the module or theme web directory
}

// Key returns Module::name or name.
func (f FileID) Key() string {
	if f.Module == "" {
		return f.Name
	}
	return f.Module + "::" + f.Name
}

// Rel returns the path relative to the package directory.
func (f FileID) Rel() string {
	if f.Module == "" {
		return f.Name
	}
	return f.Module + "/" + f.Name
}

// collectFiles returns all files of the package, like Magento's Collector together with
// the package inheritance (lib, module base and area files, theme chain, locale files).
func (p *Package) collectFiles() []FileID {
	env := p.d.env
	seen := map[string]bool{}
	var files []FileID
	add := func(module, name string) {
		key := module + "::" + name
		if seen[key] {
			return
		}
		seen[key] = true
		files = append(files, FileID{Module: module, Name: name})
	}

	for _, f := range p.d.index.List(p.libDir()) {
		if strings.HasPrefix(f, "css/docs") {
			continue
		}
		add("", f)
	}

	for _, m := range env.RegisteredModules {
		if !env.IsEnabled(m.Name) {
			continue
		}
		for _, area := range []string{"base", p.Area} {
			webDir := filepath.Join(m.Path, "view", area, "web")
			for _, f := range p.d.index.List(webDir) {
				add(m.Name, f)
			}
			for _, f := range p.d.index.List(filepath.Join(webDir, "i18n", p.Locale)) {
				add(m.Name, f)
			}
		}
	}

	for _, t := range p.Theme.Chain() {
		for _, f := range p.d.index.List(filepath.Join(t.Path, "web")) {
			if name, ok := p.stripThemeLocale(f); ok {
				add("", name)
			}
		}
		for _, mod := range p.themeModules(t) {
			if !env.IsEnabled(mod) {
				continue
			}
			for _, f := range p.d.index.List(filepath.Join(t.Path, mod, "web")) {
				if name, ok := p.stripThemeLocale(f); ok {
					add(mod, name)
				}
			}
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Rel() < files[j].Rel() })
	return files
}

// stripThemeLocale handles theme files below web/i18n/<locale>/: these belong to the
// package of that locale only.
func (p *Package) stripThemeLocale(f string) (string, bool) {
	if !strings.HasPrefix(f, "i18n/") {
		return f, true
	}
	parts := strings.SplitN(f, "/", 3)
	if len(parts) < 3 || !localeDirRegex.MatchString(parts[1]) {
		return f, true
	}
	if parts[1] != p.Locale {
		return "", false
	}
	return parts[2], true
}

// hasLocaleSpecificFiles tells whether any i18n/<locale> fallback directory contains files.
func (p *Package) hasLocaleSpecificFiles() bool {
	for _, t := range p.Theme.Chain() {
		if len(p.d.index.List(filepath.Join(t.Path, "web", "i18n", p.Locale))) > 0 {
			return true
		}
		for _, mod := range p.themeModules(t) {
			if len(p.d.index.List(filepath.Join(t.Path, mod, "web", "i18n", p.Locale))) > 0 {
				return true
			}
		}
	}
	for _, m := range p.d.env.RegisteredModules {
		if !p.d.env.IsEnabled(m.Name) {
			continue
		}
		for _, area := range []string{"base", p.Area} {
			if len(p.d.index.List(filepath.Join(m.Path, "view", area, "web", "i18n", p.Locale))) > 0 {
				return true
			}
		}
	}
	return false
}

// deployedName maps a source file name to the deployed file name (.less becomes .css,
// except for partials starting with an underscore).
func deployedName(name string) string {
	if path.Ext(name) == ".less" && !strings.HasPrefix(path.Base(name), "_") {
		return strings.TrimSuffix(name, ".less") + ".css"
	}
	return name
}
