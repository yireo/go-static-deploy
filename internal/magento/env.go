// Package magento contains the knowledge about a Magento installation that is needed
// to deploy static content: registered components, enabled modules, themes and
// the relevant system configuration.
package magento

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yireo/go-static-deploy/internal/phpbridge"
)

// Component is a registered Magento component (module, theme, language or library).
type Component struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type rawConfig struct {
	PHPVersion string `json:"php_version"`
	Mode       string `json:"mode"`
	Modules    []struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	} `json:"modules"`
	Components                      map[string][]Component `json:"components"`
	SystemDefault                   map[string]any         `json:"system_default"`
	DBDefault                       map[string]any         `json:"db_default"`
	DefaultStoreID                  int                    `json:"default_store_id"`
	StaticContentOnlyEnabledModules bool                   `json:"static_content_only_enabled_modules"`
	HasLessPHP                      bool                   `json:"has_less_php"`
	HasIntl                         bool                   `json:"has_intl"`
	AssetContentTrimmed             bool                   `json:"asset_content_trimmed"`
	LessSourceMaps                  bool                   `json:"less_source_maps"`
	MinifyExcludePlugins            struct {
		JS       []string `json:"js"`
		CSS      []string `json:"css"`
		Warnings []string `json:"warnings"`
	} `json:"minify_exclude_plugins"`
}

// Theme is a registered theme.
type Theme struct {
	Area       string // frontend, adminhtml
	Code       string // Vendor/name
	Path       string // absolute path
	ParentCode string
	Parent     *Theme
}

// FullPath returns area/Vendor/name.
func (t *Theme) FullPath() string { return t.Area + "/" + t.Code }

// Chain returns the theme and its ancestors, the theme itself first.
func (t *Theme) Chain() []*Theme {
	var chain []*Theme
	seen := map[*Theme]bool{}
	for cur := t; cur != nil && !seen[cur]; cur = cur.Parent {
		seen[cur] = true
		chain = append(chain, cur)
	}
	return chain
}

// InheritedThemes returns the theme chain with the root theme first (Magento's getInheritedThemes()).
func (t *Theme) InheritedThemes() []*Theme {
	chain := t.Chain()
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

// Env describes a Magento installation.
type Env struct {
	Root       string
	Mode       string
	PHPVersion string

	// EnabledModules contains the enabled modules in config.php order (module sequence order).
	EnabledModules []string
	enabled        map[string]bool
	modulePriority map[string]int
	ModulePaths    map[string]string
	// RegisteredModules contains all registered modules in registration order.
	RegisteredModules []Component

	Themes        map[string]*Theme // key: area/Vendor/name
	LanguagePacks []Component

	Config *Config

	DefaultStoreID                  int
	StaticContentOnlyEnabledModules bool
	HasLessPHP                      bool
	HasIntl                         bool

	// MinifyExcludePlugins contains the minification excludes added by plugins on
	// Magento\Framework\View\Asset\Minification::getExcludes(), per content type (js, css).
	MinifyExcludePlugins map[string][]string
	// AssetContentTrimmed tells whether Magento trims the content of imported LESS files
	// (Magento\Framework\View\Asset\File::getContent() in Magento 2.4.9 and later).
	AssetContentTrimmed bool
	// LessSourceMaps tells whether Magento's LESS processor generates source maps in developer mode
	// (Magento\Framework\Css\PreProcessor\Adapter\Less\Processor in Magento 2.4.8 and later).
	LessSourceMaps bool
	// Warnings are problems found while reading the installation details.
	Warnings []string
}

// Load reads the installation details using the PHP bridge and the filesystem.
func Load(bridge *phpbridge.Bridge) (*Env, error) {
	var raw rawConfig
	if err := bridge.Run("config", &raw); err != nil {
		return nil, err
	}

	env := &Env{
		Root:                            bridge.Root,
		Mode:                            raw.Mode,
		PHPVersion:                      raw.PHPVersion,
		enabled:                         map[string]bool{},
		modulePriority:                  map[string]int{},
		ModulePaths:                     map[string]string{},
		Themes:                          map[string]*Theme{},
		LanguagePacks:                   raw.Components["language"],
		DefaultStoreID:                  raw.DefaultStoreID,
		StaticContentOnlyEnabledModules: raw.StaticContentOnlyEnabledModules,
		HasLessPHP:                      raw.HasLessPHP,
		HasIntl:                         raw.HasIntl,
		MinifyExcludePlugins: map[string][]string{
			"js":  raw.MinifyExcludePlugins.JS,
			"css": raw.MinifyExcludePlugins.CSS,
		},
		Warnings:            raw.MinifyExcludePlugins.Warnings,
		AssetContentTrimmed: raw.AssetContentTrimmed,
		LessSourceMaps:      raw.LessSourceMaps,
	}
	if env.DefaultStoreID == 0 {
		env.DefaultStoreID = 1
	}

	for _, m := range raw.Components["module"] {
		env.ModulePaths[m.Name] = filepath.Clean(m.Path)
		env.RegisteredModules = append(env.RegisteredModules, Component{Name: m.Name, Path: filepath.Clean(m.Path)})
	}
	for _, m := range raw.Modules {
		if !m.Enabled {
			continue
		}
		if _, ok := env.ModulePaths[m.Name]; !ok {
			continue
		}
		env.modulePriority[m.Name] = len(env.EnabledModules)
		env.EnabledModules = append(env.EnabledModules, m.Name)
		env.enabled[m.Name] = true
	}

	for _, t := range raw.Components["theme"] {
		parts := strings.SplitN(t.Name, "/", 2)
		if len(parts) != 2 {
			continue
		}
		theme := &Theme{Area: parts[0], Code: parts[1], Path: filepath.Clean(t.Path)}
		theme.ParentCode = readThemeParent(theme.Path)
		env.Themes[t.Name] = theme
	}
	for _, theme := range env.Themes {
		if theme.ParentCode != "" {
			theme.Parent = env.Themes[theme.Area+"/"+theme.ParentCode]
		}
	}

	cfg, err := loadConfig(env, raw.SystemDefault, raw.DBDefault)
	if err != nil {
		return nil, err
	}
	env.Config = cfg
	return env, nil
}

// NewEnv creates an environment programmatically (used by tests and tools). Themes are
// linked to their parents by ParentCode.
func NewEnv(root, mode string, modules []Component, enabledModules []string, themes []*Theme, cfg *Config) *Env {
	env := &Env{
		Root:                root,
		Mode:                mode,
		enabled:             map[string]bool{},
		modulePriority:      map[string]int{},
		ModulePaths:         map[string]string{},
		Themes:              map[string]*Theme{},
		Config:              cfg,
		DefaultStoreID:      1,
		HasLessPHP:          true,
		AssetContentTrimmed: true,
		LessSourceMaps:      true,
	}
	if env.Config == nil {
		env.Config = NewConfig()
	}
	for _, m := range modules {
		env.ModulePaths[m.Name] = m.Path
		env.RegisteredModules = append(env.RegisteredModules, m)
	}
	for _, name := range enabledModules {
		env.modulePriority[name] = len(env.EnabledModules)
		env.EnabledModules = append(env.EnabledModules, name)
		env.enabled[name] = true
	}
	for _, t := range themes {
		env.Themes[t.FullPath()] = t
	}
	for _, t := range themes {
		if t.ParentCode != "" {
			t.Parent = env.Themes[t.Area+"/"+t.ParentCode]
		}
	}
	return env
}

// IsEnabled tells whether a module is enabled.
func (e *Env) IsEnabled(module string) bool { return e.enabled[module] }

// ModulePriority returns the position of the module in the module list, or -1.
func (e *Env) ModulePriority(module string) int {
	if p, ok := e.modulePriority[module]; ok {
		return p
	}
	return -1
}

// IsDeveloperMode tells whether Magento runs in developer mode.
func (e *Env) IsDeveloperMode() bool { return e.Mode == "developer" }

// Theme returns a theme by area and code.
func (e *Env) Theme(area, code string) *Theme { return e.Themes[area+"/"+code] }

// ThemesForArea returns all themes of an area, sorted by code.
func (e *Env) ThemesForArea(area string) []*Theme {
	var result []*Theme
	for _, t := range e.Themes {
		if t.Area == area {
			result = append(result, t)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Code < result[j].Code })
	return result
}

// Areas returns all areas that have themes.
func (e *Env) Areas() []string {
	seen := map[string]bool{}
	var areas []string
	for _, t := range e.Themes {
		if !seen[t.Area] {
			seen[t.Area] = true
			areas = append(areas, t.Area)
		}
	}
	sort.Strings(areas)
	return areas
}

func readThemeParent(themePath string) string {
	data, err := os.ReadFile(filepath.Join(themePath, "theme.xml"))
	if err != nil {
		return ""
	}
	var doc struct {
		Parent string `xml:"parent"`
	}
	if err := xml.Unmarshal(data, &doc); err != nil {
		return ""
	}
	return strings.TrimSpace(doc.Parent)
}

// IsHyva tells whether a theme is (based on) a Hyvä theme.
func (t *Theme) IsHyva() bool {
	for _, cur := range t.Chain() {
		if strings.HasPrefix(cur.Code, "Hyva/") {
			return true
		}
		for _, f := range []string{"web/tailwind/tailwind.config.js", "web/tailwind/tailwind-source.css"} {
			if _, err := os.Stat(filepath.Join(cur.Path, f)); err == nil {
				return true
			}
		}
	}
	return false
}

// String implements fmt.Stringer.
func (t *Theme) String() string { return fmt.Sprintf("%s/%s", t.Area, t.Code) }
