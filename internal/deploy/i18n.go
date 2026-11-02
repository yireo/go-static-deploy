package deploy

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
)

const jsTranslationFile = "js-translation.json"

// phraseFiles mirrors the file list of Magento\Translation\Model\Js\DataProvider::getData():
// JS and HTML files of all modules and all themes of the area (and of the base area).
func (d *Deployer) phraseFiles(area string) []string {
	d.phraseMu.Lock()
	defer d.phraseMu.Unlock()
	if files, ok := d.phraseCache[area]; ok {
		return files
	}
	var files []string
	addMatching := func(dir, suffix string) {
		for _, f := range d.index.List(dir) {
			if strings.HasSuffix(f, suffix) {
				files = append(files, filepath.Join(dir, filepath.FromSlash(f)))
			}
		}
	}
	lib := filepath.Join(d.env.Root, "lib", "web")
	addMatching(filepath.Join(lib, "mage"), ".js")
	addMatching(filepath.Join(lib, "varien"), ".js")

	for _, a := range []string{"base", area} {
		for _, t := range d.env.ThemesForArea(a) {
			addMatching(filepath.Join(t.Path, "web"), ".js")
			for _, sub := range d.index.Subdirs(t.Path) {
				if strings.Contains(sub, "_") {
					addMatching(filepath.Join(t.Path, sub, "web"), ".js")
				}
			}
		}
		for _, m := range d.env.RegisteredModules {
			addMatching(filepath.Join(m.Path, "view", a, "web"), ".js")
		}
	}
	for _, a := range []string{"base", area} {
		for _, t := range d.env.ThemesForArea(a) {
			addMatching(filepath.Join(t.Path, "web", "template"), ".html")
			for _, sub := range d.index.Subdirs(t.Path) {
				if strings.Contains(sub, "_") {
					addMatching(filepath.Join(t.Path, sub, "web", "template"), ".html")
				}
			}
		}
		for _, m := range d.env.RegisteredModules {
			addMatching(filepath.Join(m.Path, "view", a, "web", "template"), ".html")
			addMatching(filepath.Join(m.Path, "view", a, "web", "templates"), ".html")
		}
	}
	d.phraseCache[area] = files
	return files
}

// translationPatterns reads the phrase patterns of Magento\Translation\Model\Js\Config from di.xml files.
func (d *Deployer) translationPatterns() []string {
	d.patternOnce.Do(func() {
		type item struct {
			Name  string `xml:"name,attr"`
			Value string `xml:",chardata"`
		}
		type argument struct {
			Name  string `xml:"name,attr"`
			Items []item `xml:"item"`
		}
		type typ struct {
			Name      string     `xml:"name,attr"`
			Arguments []argument `xml:"arguments>argument"`
		}
		type diConfig struct {
			Types []typ `xml:"type"`
		}
		values := map[string]string{}
		var order []string
		for _, module := range d.env.EnabledModules {
			data, err := os.ReadFile(filepath.Join(d.env.ModulePaths[module], "etc", "di.xml"))
			if err != nil || !strings.Contains(string(data), `Magento\Translation\Model\Js\Config`) {
				continue
			}
			var cfg diConfig
			if err := xml.Unmarshal(data, &cfg); err != nil {
				continue
			}
			for _, t := range cfg.Types {
				if strings.TrimPrefix(t.Name, `\`) != `Magento\Translation\Model\Js\Config` {
					continue
				}
				for _, arg := range t.Arguments {
					if arg.Name != "patterns" {
						continue
					}
					for _, it := range arg.Items {
						if _, ok := values[it.Name]; !ok {
							order = append(order, it.Name)
						}
						values[it.Name] = it.Value
					}
				}
			}
		}
		for _, name := range order {
			d.patterns = append(d.patterns, values[name])
		}
	})
	return d.patterns
}

// jsTranslation builds js-translation.json using the PHP worker.
func (p *Package) jsTranslation() ([]byte, error) {
	env := p.d.env
	var moduleCsvs []string
	for _, module := range env.EnabledModules {
		moduleCsvs = append(moduleCsvs, filepath.Join(env.ModulePaths[module], "i18n", p.Locale+".csv"))
	}
	var packs []string
	for _, l := range env.LanguagePacks {
		packs = append(packs, l.Path)
	}
	var themeCsvs []string
	for _, t := range p.Theme.InheritedThemes() {
		found := ""
		for _, c := range t.Chain() {
			f := filepath.Join(c.Path, "i18n", p.Locale+".csv")
			if fileExists(f) {
				found = f
				break
			}
		}
		themeCsvs = append(themeCsvs, found)
	}
	storeIDs := []int{0}
	if p.Area != "adminhtml" {
		storeIDs = append(storeIDs, env.DefaultStoreID)
	}
	resp, err := p.d.php.Call(map[string]any{
		"cmd":            "i18n",
		"locale":         p.Locale,
		"area":           p.Area,
		"module_csvs":    moduleCsvs,
		"language_packs": packs,
		"theme_csvs":     themeCsvs,
		"phrase_files":   p.d.phraseFiles(p.Area),
		"patterns":       p.d.translationPatterns(),
		"with_db":        p.d.withDB,
		"store_ids":      storeIDs,
	})
	if err != nil {
		return nil, err
	}
	return []byte(resp.Content), nil
}
