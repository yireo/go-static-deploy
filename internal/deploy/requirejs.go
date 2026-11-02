package deploy

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	requireJsConfigFile  = "requirejs-config.js"
	requireJsMinResolver = "requirejs-min-resolver.js"
)

// requireJsConfigFiles mirrors Magento\Framework\RequireJs\Config\File\Collector\Aggregated::getFiles().
func (p *Package) requireJsConfigFiles() []string {
	env := p.d.env
	var files []string

	libFile := filepath.Join(p.libDir(), requireJsConfigFile)
	if fileExists(libFile) {
		files = append(files, libFile)
	}

	type moduleFile struct {
		module string
		path   string
	}
	sortByModule := func(list []moduleFile) {
		sort.SliceStable(list, func(i, j int) bool {
			a, b := list[i], list[j]
			if a.module == b.module {
				return false
			}
			pa, pb := env.ModulePriority(a.module), env.ModulePriority(b.module)
			if pa == pb {
				return a.module < b.module
			}
			return pa < pb
		})
	}

	var base []moduleFile
	for _, area := range []string{"base", p.Area} {
		for _, m := range env.RegisteredModules {
			f := filepath.Join(m.Path, "view", area, requireJsConfigFile)
			if env.IsEnabled(m.Name) && fileExists(f) {
				base = append(base, moduleFile{module: m.Name, path: f})
			}
		}
	}
	sortByModule(base)
	for _, f := range base {
		files = append(files, f.path)
	}

	for _, t := range p.Theme.InheritedThemes() {
		var modular []moduleFile
		for _, mod := range p.d.index.Subdirs(t.Path) {
			if !strings.Contains(mod, "_") || !env.IsEnabled(mod) {
				continue
			}
			f := filepath.Join(t.Path, mod, requireJsConfigFile)
			if fileExists(f) {
				modular = append(modular, moduleFile{module: mod, path: f})
			}
		}
		sortByModule(modular)
		for _, f := range modular {
			files = append(files, f.path)
		}
		if f := filepath.Join(t.Path, requireJsConfigFile); fileExists(f) {
			files = append(files, f)
		}
	}
	return files
}

// requireJsConfig mirrors Magento\Framework\RequireJs\Config::getConfig().
func (p *Package) requireJsConfig() ([]byte, error) {
	var sb strings.Builder
	sb.WriteString("(function(require){\n")
	for _, f := range p.requireJsConfigFiles() {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		sb.WriteString("(function() {\n")
		sb.Write(data)
		sb.WriteString("\nrequire.config(config);\n})();\n")
	}
	sb.WriteString("\n\n\n})(require);")
	return []byte(sb.String()), nil
}

// requireJsMinResolver mirrors Magento\Framework\RequireJs\Config::getMinResolverCode().
func (p *Package) requireJsMinResolver() []byte {
	excludes := []string{"url.indexOf(baseUrl)===0"}
	for _, expr := range p.d.minify.excludesFor("js") {
		excludes = append(excludes, "!url.match(/"+strings.ReplaceAll(expr, "/", `\/`)+"/)")
	}
	code := `    (function () {
        var ctx = require.s.contexts._,
            origNameToUrl = ctx.nameToUrl,
            baseUrl = ctx.config.baseUrl;

        ctx.nameToUrl = function() {
            var url = origNameToUrl.apply(ctx, arguments);
            if (` + strings.Join(excludes, "&&") + `) {
                url = url.replace(/(\.min)?\.js$/, '.min.js');
            }
            return url;
        };
    })();`
	return []byte(code)
}

func fileExists(f string) bool {
	info, err := os.Stat(f)
	return err == nil && !info.IsDir()
}
