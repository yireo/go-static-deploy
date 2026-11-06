// Package deploy implements Magento's static content deployment.
package deploy

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yireo/go-static-deploy/internal/magento"
	"github.com/yireo/go-static-deploy/internal/phpbridge"
)

// Deployer deploys static content for a set of packages.
type Deployer struct {
	env    *magento.Env
	opts   Options
	index  *Index
	php    *phpbridge.Pool
	withDB bool
	minify *minification
	mode   string
	out    io.Writer
	logMu  sync.Mutex

	ioSem   chan struct{}
	lessSem chan struct{}

	phraseMu    sync.Mutex
	phraseCache map[string][]string
	patternOnce sync.Once
	patterns    []string

	sriNoticeOnce sync.Once
	sriMu         sync.Mutex
	sriHashes     map[string]map[string]string // per area, for sri-hashes.json per area

	sriLayoutOnce  sync.Once
	sriLayoutValue string

	lessCache sync.Map

	viewMu    sync.Mutex
	viewCache map[*magento.Theme]*bundleConfig
}

// Result describes the deployment of one package.
type Result struct {
	Package   string
	Hyva      bool
	Files     int64
	Duration  time.Duration
	Symlinked string
	Warnings  []string
	Errors    []string
}

// New creates a deployer.
func New(env *magento.Env, pool *phpbridge.Pool, withDB bool, opts Options, out io.Writer) *Deployer {
	if opts.Jobs < 1 {
		opts.Jobs = 1
	}
	mode := env.Mode
	if opts.Mode != "" {
		mode = opts.Mode
	}
	developer := mode == "developer"
	cfg := env.Config
	d := &Deployer{
		env:         env,
		opts:        opts,
		index:       NewIndex(),
		php:         pool,
		withDB:      withDB,
		mode:        mode,
		out:         out,
		ioSem:       make(chan struct{}, opts.Jobs*8),
		lessSem:     make(chan struct{}, opts.Jobs),
		phraseCache: map[string][]string{},
		viewCache:   map[*magento.Theme]*bundleConfig{},
		sriHashes:   map[string]map[string]string{},
	}
	d.minify = newMinification(
		!developer && cfg.Flag("dev/js/minify_files"),
		!developer && cfg.Flag("dev/css/minify_files"),
		append(cfg.List("dev/js/minify_exclude"), env.MinifyExcludePlugins["js"]...),
		append(cfg.List("dev/css/minify_exclude"), env.MinifyExcludePlugins["css"]...),
	)
	return d
}

// Mode returns the effective Magento mode.
func (d *Deployer) Mode() string { return d.mode }

func (d *Deployer) logf(format string, args ...any) {
	d.logMu.Lock()
	defer d.logMu.Unlock()
	fmt.Fprintf(d.out, format, args...)
}

func included(value string, include, exclude []string) bool {
	if len(exclude) > 0 && !(len(exclude) == 1 && exclude[0] == "none") && slices.Contains(exclude, value) {
		return false
	}
	if len(include) == 0 || slices.Contains(include, "all") {
		return true
	}
	return slices.Contains(include, value)
}

// Packages returns the packages to deploy, grouped per area/theme with locales in the given order.
func (d *Deployer) Packages() ([][]*Package, error) {
	locales := d.opts.Locales
	if len(locales) == 0 || slices.Contains(locales, "all") {
		locales = []string{"en_US"}
	}
	var groups [][]*Package
	foundThemes := map[string]bool{}
	for _, area := range d.env.Areas() {
		if !included(area, d.opts.Areas, d.opts.ExcludeAreas) {
			continue
		}
		for _, theme := range d.env.ThemesForArea(area) {
			if !included(theme.Code, d.opts.Themes, d.opts.ExcludeThemes) {
				continue
			}
			foundThemes[theme.Code] = true
			var group []*Package
			for _, locale := range locales {
				if !included(locale, nil, d.opts.ExcludeLocales) {
					continue
				}
				group = append(group, newPackage(d, area, theme, locale))
			}
			if len(group) > 0 {
				groups = append(groups, group)
			}
		}
	}
	for _, t := range d.opts.Themes {
		if t != "all" && !foundThemes[t] {
			d.logf("Warning: theme %s is not registered for the selected areas, skipping\n", t)
		}
	}
	return groups, nil
}

// WriteVersion writes pub/static/deployed_version.txt.
func (d *Deployer) WriteVersion() (string, error) {
	version := d.opts.ContentVersion
	if version == "" {
		version = strconv.FormatInt(time.Now().Unix(), 10)
	}
	dir := filepath.Join(d.env.Root, "pub", "static")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return version, os.WriteFile(filepath.Join(dir, "deployed_version.txt"), []byte(version), 0o644)
}

// Run deploys all packages.
func (d *Deployer) Run(groups [][]*Package) []Result {
	if d.sriEnabled() && d.sriLayout() != sriLayoutPackage {
		d.removeSRIHashes()
	}
	var results []Result
	var mu sync.Mutex
	sem := make(chan struct{}, d.opts.Jobs)
	var wg sync.WaitGroup

	run := func(p *Package, base *Package, baseResult *Result) {
		defer wg.Done()
		sem <- struct{}{}
		defer func() { <-sem }()
		r := d.deployPackage(p, base, baseResult)
		mu.Lock()
		results = append(results, r)
		mu.Unlock()
		if d.opts.Verbose {
			d.printResult(r)
		}
	}

	localeMode := d.opts.SymlinkMode == SymlinkLocale || d.opts.SymlinkMode == SymlinkLocaleContained
	baseResults := make([]*Result, len(groups))
	for i, group := range groups {
		if !localeMode {
			for _, p := range group {
				wg.Add(1)
				go run(p, nil, nil)
			}
			continue
		}
		wg.Add(1)
		go func(i int, p *Package) {
			defer wg.Done()
			sem <- struct{}{}
			r := d.deployPackage(p, nil, nil)
			<-sem
			mu.Lock()
			results = append(results, r)
			baseResults[i] = &r
			mu.Unlock()
			if d.opts.Verbose {
				d.printResult(r)
			}
		}(i, group[0])
	}
	wg.Wait()

	if localeMode {
		for i, group := range groups {
			for _, p := range group[1:] {
				wg.Add(1)
				go run(p, group[0], baseResults[i])
			}
		}
		wg.Wait()
	}

	order := map[string]int{}
	n := 0
	for _, group := range groups {
		for _, p := range group {
			order[p.Path()] = n
			n++
		}
	}
	slices.SortFunc(results, func(a, b Result) int { return order[a.Package] - order[b.Package] })
	return results
}

func (d *Deployer) printResult(r Result) {
	switch {
	case len(r.Errors) > 0:
		d.logf("✗ %s - %d files - %.2fs - %d errors\n", r.Package, r.Files, r.Duration.Seconds(), len(r.Errors))
	case r.Symlinked != "":
		d.logf("✓ %s → %s (symlink) - %.2fs\n", r.Package, r.Symlinked, r.Duration.Seconds())
	default:
		d.logf("✓ %s - %d files - %.2fs\n", r.Package, r.Files, r.Duration.Seconds())
	}
}

type entryKind int

const (
	kindCopy entryKind = iota
	kindJS
	kindCSS
	kindLess
)

type entry struct {
	kind     entryKind
	module   string
	name     string // deployed name (without minification sign)
	target   string // path relative to the package directory
	source   string
	srcIsMin bool
}

func (e entry) rel() string { return FileID{Module: e.module, Name: e.name}.Rel() }

// plan creates the list of files to deploy for a package.
func (p *Package) plan() ([]entry, []string) {
	d := p.d
	var warnings []string
	byTarget := map[string]bool{}
	var entries []entry
	for _, f := range p.collectFiles() {
		name := deployedName(f.Name)
		ext := path.Ext(name)
		if ext == ".less" {
			continue
		}
		e := entry{module: f.Module, name: name}
		switch ext {
		case ".css":
			if d.opts.NoCSS {
				continue
			}
			src, isMin := p.ResolveMin(f.Module, name)
			if src != "" {
				e.kind, e.source, e.srcIsMin = kindCSS, src, isMin
			} else if !d.opts.NoLess {
				lessName := strings.TrimSuffix(name, ".css") + ".less"
				src = p.Resolve(f.Module, lessName)
				if src == "" {
					warnings = append(warnings, "unable to resolve "+FileID{f.Module, name}.Key())
					continue
				}
				e.kind, e.source = kindLess, src
			} else {
				continue
			}
			e.target = d.minifiedName(p.Path(), e.rel())
		case ".js":
			if d.opts.NoJavascript {
				continue
			}
			src, isMin := p.ResolveMin(f.Module, name)
			if src == "" {
				warnings = append(warnings, "unable to resolve "+FileID{f.Module, name}.Key())
				continue
			}
			e.kind, e.source, e.srcIsMin = kindJS, src, isMin
			e.target = d.minifiedName(p.Path(), e.rel())
		default:
			src := p.Resolve(f.Module, name)
			if src == "" {
				warnings = append(warnings, "unable to resolve "+FileID{f.Module, name}.Key())
				continue
			}
			e.kind, e.source, e.target = kindCopy, src, e.rel()
		}
		if byTarget[e.target] {
			continue
		}
		byTarget[e.target] = true
		entries = append(entries, e)
	}
	return entries, warnings
}

func (d *Deployer) packageDirs(p *Package) (string, string) {
	out := filepath.Join(d.env.Root, "pub", "static", filepath.FromSlash(p.Path()))
	tmp := filepath.Join(d.env.Root, "var", "view_preprocessed", "pub", "static", filepath.FromSlash(p.Path()))
	return out, tmp
}

type packageRun struct {
	p        *Package
	outDir   string
	symlink  bool
	files    atomic.Int64
	mu       sync.Mutex
	errors   []string
	warnings []string
}

func (r *packageRun) errorf(format string, args ...any) {
	r.mu.Lock()
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
	r.mu.Unlock()
}

func (d *Deployer) deployPackage(p *Package, base *Package, baseResult *Result) Result {
	start := time.Now()
	outDir, tmpDir := d.packageDirs(p)
	result := Result{Package: p.Path(), Hyva: p.Theme.IsHyva()}

	if !d.opts.NoClean {
		_ = os.RemoveAll(outDir)
		_ = os.RemoveAll(tmpDir)
	}

	if base != nil && baseResult != nil && len(baseResult.Errors) == 0 {
		if ok, reason := d.canSymlinkLocale(p, base); ok {
			if err := os.MkdirAll(filepath.Dir(outDir), 0o755); err == nil {
				if err := os.Symlink(base.Locale, outDir); err == nil {
					result.Symlinked = base.Locale
					result.Files = baseResult.Files
					result.Duration = time.Since(start)
					return result
				}
			}
		} else if d.opts.Verbose {
			result.Warnings = append(result.Warnings, "not symlinking locale: "+reason)
		}
	}

	run := &packageRun{p: p, outDir: outDir}
	switch d.opts.SymlinkMode {
	case SymlinkFile:
		run.symlink = true
	case SymlinkLocale:
		run.symlink = true
	}

	entries, warnings := p.plan()
	run.warnings = append(run.warnings, warnings...)

	var wg sync.WaitGroup
	var lessEntries []entry
	for _, e := range entries {
		if e.kind == kindLess {
			lessEntries = append(lessEntries, e)
			continue
		}
		wg.Add(1)
		d.ioSem <- struct{}{}
		go func(e entry) {
			defer wg.Done()
			defer func() { <-d.ioSem }()
			if err := d.deployEntry(run, e); err != nil {
				run.errorf("%s: %v", e.target, err)
			}
		}(e)
	}

	if len(lessEntries) > 0 {
		m := newLessMaterializer(p)
		for _, e := range lessEntries {
			root := asset{Module: e.module, FilePath: strings.TrimSuffix(e.name, ".css") + ".less"}
			file, hash, err := m.Materialize(root, e.source)
			if err != nil {
				run.errorf("%s: %v", e.target, err)
				continue
			}
			wg.Add(1)
			go func(e entry, file, hash string) {
				defer wg.Done()
				if err := d.compileLess(run, e, file, hash); err != nil {
					run.errorf("Compilation from source %s: %v", e.source, err)
				}
			}(e, file, hash)
		}
		run.warnings = append(run.warnings, m.warnings...)
	}
	wg.Wait()

	if !d.opts.NoJavascript {
		if err := d.writeRequireJs(run); err != nil {
			run.errorf("requirejs-config.js: %v", err)
		}
		if !d.opts.NoTranslations {
			content, err := p.jsTranslation()
			if err != nil {
				run.errorf("%s: %v", jsTranslationFile, err)
			} else if err := d.writeOutput(run, jsTranslationFile, content); err != nil {
				run.errorf("%s: %v", jsTranslationFile, err)
			}
		}
	}

	if !d.opts.NoJavascript && !d.opts.NoJsBundle {
		if err := p.bundle(); err != nil {
			run.errorf("js/bundle: %v", err)
		}
	}

	if d.sriEnabled() {
		if err := p.writeSRIHashes(outDir, entries); err != nil {
			run.errorf("%s: %v", sriFile, err)
		}
	}

	result.Files = run.files.Load()
	result.Errors = run.errors
	result.Warnings = append(result.Warnings, run.warnings...)
	result.Duration = time.Since(start)
	return result
}

// canSymlinkLocale tells whether a locale package can be a directory symlink to the base locale.
func (d *Deployer) canSymlinkLocale(p, base *Package) (bool, string) {
	if d.mode == "developer" {
		return false, "developer mode (source maps contain the locale)"
	}
	if d.sriEnabled() {
		d.sriNoticeOnce.Do(func() {
			d.logf("Notice: sri-hashes.json contains locale specific paths, so additional locales are deployed in full instead of as directory symlinks\n")
		})
		return false, "SRI hashes are locale specific"
	}
	if p.hasLocaleSpecificFiles() || base.hasLocaleSpecificFiles() {
		return false, "locale specific files found"
	}
	if !d.opts.NoJavascript && !d.opts.NoTranslations {
		own, err := p.jsTranslation()
		if err != nil {
			return false, err.Error()
		}
		baseOut, _ := d.packageDirs(base)
		baseContent, err := os.ReadFile(filepath.Join(baseOut, jsTranslationFile))
		if err != nil || !bytes.Equal(own, baseContent) {
			return false, "translations differ"
		}
	}
	return true, ""
}

func (d *Deployer) deployEntry(run *packageRun, e entry) error {
	switch e.kind {
	case kindJS:
		if e.target != e.rel() && !e.srcIsMin {
			data, err := os.ReadFile(e.source)
			if err != nil {
				return err
			}
			return d.writeOutput(run, e.target, d.minify.minifyJS(data))
		}
		return d.placeFile(run, e.source, e.target)
	case kindCSS:
		data, err := os.ReadFile(e.source)
		if err != nil {
			return err
		}
		changed := false
		if bytes.Contains(data, []byte("{{")) || bytes.Contains(data, []byte("::")) {
			content := processCSS(string(data), run.p.Area, run.p.Theme.Code, run.p.Path(), e.rel())
			if content != string(data) {
				data, changed = []byte(content), true
			}
		}
		if e.target != e.rel() && !e.srcIsMin {
			data, changed = d.minify.minifyCSS(data), true
		}
		if !changed {
			return d.placeFile(run, e.source, e.target)
		}
		return d.writeOutput(run, e.target, data)
	default:
		return d.placeFile(run, e.source, e.target)
	}
}

type lessResult struct {
	once    sync.Once
	content []byte
	err     error
}

// compileLess compiles a materialized LESS file. Outside developer mode (no source maps)
// the result only depends on the inputs, so it is shared between the locales of a theme.
func (d *Deployer) compileLess(run *packageRun, e entry, file, hash string) error {
	compile := func() ([]byte, error) {
		d.lessSem <- struct{}{}
		defer func() { <-d.lessSem }()
		var content string
		if d.opts.LessCompiler == LessCompilerPHP {
			resp, err := d.php.Call(map[string]any{
				"cmd":         "less",
				"file":        file,
				"mode":        d.mode,
				"source_maps": d.env.LessSourceMaps,
				"asset_path":  e.rel(),
			})
			if err != nil {
				return nil, err
			}
			content = resp.Content
		} else {
			var err error
			if content, err = compileLessGo(file, e.rel(), d.mode != "developer"); err != nil {
				return nil, err
			}
		}
		return []byte(processCSS(content, run.p.Area, run.p.Theme.Code, run.p.Path(), e.rel())), nil
	}
	var data []byte
	var err error
	if d.mode == "developer" {
		data, err = compile()
	} else {
		key := run.p.Area + "|" + run.p.Theme.Code + "|" + e.rel() + "|" + hash
		value, _ := d.lessCache.LoadOrStore(key, &lessResult{})
		result := value.(*lessResult)
		result.once.Do(func() { result.content, result.err = compile() })
		data, err = result.content, result.err
	}
	if err != nil {
		return err
	}
	if e.target != e.rel() {
		data = d.minify.minifyCSS(data)
	}
	return d.writeOutput(run, e.target, data)
}

func (d *Deployer) writeRequireJs(run *packageRun) error {
	config, err := run.p.requireJsConfig()
	if err != nil {
		return err
	}
	resolver := run.p.requireJsMinResolver()
	if d.minify.isEnabled("js") {
		config = d.minify.minifyJS(config)
		resolver = d.minify.minifyJS(resolver)
	}
	if err := d.writeOutput(run, d.minifiedName(run.p.Path(), requireJsConfigFile), config); err != nil {
		return err
	}
	return d.writeOutput(run, d.minifiedName(run.p.Path(), requireJsMinResolver), resolver)
}

func (d *Deployer) writeOutput(run *packageRun, rel string, data []byte) error {
	target := filepath.Join(run.outDir, filepath.FromSlash(rel))
	if err := writeFile(target, data); err != nil {
		return err
	}
	run.files.Add(1)
	return nil
}

func (d *Deployer) placeFile(run *packageRun, source, rel string) error {
	target := filepath.Join(run.outDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	_ = os.Remove(target)
	if run.symlink {
		link, err := filepath.Rel(filepath.Dir(target), source)
		if err != nil {
			link = source
		}
		if err := os.Symlink(link, target); err != nil {
			return err
		}
		run.files.Add(1)
		return nil
	}
	if err := copyFile(source, target); err != nil {
		return err
	}
	run.files.Add(1)
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
