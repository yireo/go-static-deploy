package deploy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

var (
	// Magento\Framework\Css\PreProcessor\Instruction\MagentoImport::REPLACE_PATTERN (without the
	// negative lookahead, which is checked in code)
	magentoImportRegex = regexp.MustCompile(`//@magento_import(\s+\(reference\))?\s+['"]([^"']+)['"]\s*?;`)
	absolutePathRegex  = regexp.MustCompile(`^(?:[/\\]|\w:[/\\])`)
	// Magento\Framework\Css\PreProcessor\Instruction\Import::REPLACE_PATTERN
	importRegex = regexp.MustCompile(`@import[\s]*([\(\),\w\s]*?['"][\s]*)([^\)'"]*?)([\s]*['"][\s\w]*[\)]?)[\s]*;`)
)

// asset is a file in the context of a package: a module (or none) and a file path.
type asset struct {
	Module   string
	FilePath string
}

// Path returns the path relative to pub/static (Magento's asset getPath()).
func (a asset) Path(pkgPath string) string {
	if a.Module != "" {
		return pkgPath + "/" + a.Module + "/" + a.FilePath
	}
	return pkgPath + "/" + a.FilePath
}

// lessMaterializer mirrors the LESS pre-processing of Magento (magento_import and import
// instructions) and materializes all LESS sources of a package into
// var/view_preprocessed/pub/static/<package>/, exactly where Magento puts them.
type lessMaterializer struct {
	p       *Package
	tmpRoot string
	pkgDir  string

	mu       sync.Mutex
	files    map[string]*materializedFile
	warnings []string
}

type materializedFile struct {
	content []byte
	deps    []string
}

func newLessMaterializer(p *Package) *lessMaterializer {
	tmpRoot := filepath.Join(p.d.env.Root, "var", "view_preprocessed", "pub", "static")
	return &lessMaterializer{
		p:       p,
		tmpRoot: tmpRoot,
		pkgDir:  filepath.Join(tmpRoot, filepath.FromSlash(p.Path())),
		files:   map[string]*materializedFile{},
	}
}

func (m *lessMaterializer) warn(format string, args ...any) {
	m.warnings = append(m.warnings, fmt.Sprintf(format, args...))
}

// Materialize pre-processes a root LESS file and all files it imports. It returns the
// absolute path of the materialized root file and a hash of all materialized input files
// (relative to the package), which identifies the compilation result.
func (m *lessMaterializer) Materialize(root asset, source string) (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	content, err := os.ReadFile(source)
	if err != nil {
		return "", "", err
	}
	target := m.tmpPath(root)
	if err := m.process(root, string(content), false); err != nil {
		return "", "", err
	}
	h := sha256.New()
	m.hash(h, target, map[string]bool{})
	return target, hex.EncodeToString(h.Sum(nil)), nil
}

func (m *lessMaterializer) hash(h io.Writer, target string, seen map[string]bool) {
	if seen[target] {
		return
	}
	seen[target] = true
	rel, _ := filepath.Rel(m.pkgDir, target)
	f := m.files[target]
	fmt.Fprintf(h, "%s\x00", rel)
	if f == nil {
		return
	}
	fmt.Fprintf(h, "%d\x00", len(f.content))
	h.Write(f.content)
	for _, dep := range f.deps {
		m.hash(h, dep, seen)
	}
}

func (m *lessMaterializer) tmpPath(a asset) string {
	return filepath.Join(m.tmpRoot, filepath.FromSlash(path.Clean(a.Path(m.p.Path()))))
}

type relatedFile struct {
	fileID string
	asset  asset
}

// process pre-processes a LESS file. Like Magento's File::getContent() (2.4.9 and later), the
// content of imported (related) files is trimmed.
func (m *lessMaterializer) process(a asset, content string, related bool) error {
	content = m.processMagentoImport(a, content)
	content, relatedFiles := m.processImport(a, content)
	if related {
		content = m.trimContent(content)
	}

	target := m.tmpPath(a)
	file := m.files[target]
	if file == nil {
		file = &materializedFile{content: []byte(content)}
		m.files[target] = file
		if err := writeFile(target, file.content); err != nil {
			return err
		}
	}

	for _, rel := range relatedFiles {
		relAsset := createRelated(rel.fileID, rel.asset)
		relTarget := m.tmpPath(relAsset)
		file.deps = append(file.deps, relTarget)
		if _, ok := m.files[relTarget]; ok {
			continue
		}
		src := m.p.Resolve(relAsset.Module, relAsset.FilePath)
		if src == "" {
			m.files[relTarget] = nil
			m.warn("unable to resolve LESS import %q in %s", rel.fileID, rel.asset.Path(m.p.Path()))
			continue
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		if path.Ext(relAsset.FilePath) == ".less" {
			if err := m.process(relAsset, string(data), true); err != nil {
				return err
			}
			continue
		}
		m.files[relTarget] = &materializedFile{content: []byte(m.trimContent(string(data)))}
		if err := writeFile(relTarget, m.files[relTarget].content); err != nil {
			return err
		}
	}
	return nil
}

// createRelated mirrors Magento\Framework\View\Asset\Repository::createRelated().
func createRelated(fileID string, relativeTo asset) asset {
	if module, filePath, ok := strings.Cut(fileID, "::"); ok && module != "" {
		return asset{Module: module, FilePath: filePath}
	}
	return asset{Module: relativeTo.Module, FilePath: normalizePath(phpDirname(relativeTo.FilePath) + "/" + fileID)}
}

func (m *lessMaterializer) processMagentoImport(a asset, content string) string {
	return magentoImportRegex.ReplaceAllStringFunc(content, func(match string) string {
		sub := magentoImportRegex.FindStringSubmatch(match)
		matchedFileID := sub[2]
		if absolutePathRegex.MatchString(matchedFileID) {
			return match
		}
		reference := ""
		if sub[1] != "" {
			reference = "(reference) "
		}
		resolvedPath := createRelated(matchedFileID, a).FilePath
		var sb strings.Builder
		for _, f := range m.magentoImportFiles(resolvedPath) {
			if f.module != "" {
				fmt.Fprintf(&sb, "@import %s'%s::%s';\n", reference, f.module, resolvedPath)
			} else {
				fmt.Fprintf(&sb, "@import %s'%s';\n", reference, matchedFileID)
			}
		}
		return sb.String()
	})
}

type importFile struct {
	module string
}

type orderedFiles struct {
	keys  []string
	files map[string]importFile
}

func (o *orderedFiles) add(key string, f importFile) {
	if o.files == nil {
		o.files = map[string]importFile{}
	}
	if _, ok := o.files[key]; ok {
		return
	}
	o.files[key] = f
	o.keys = append(o.keys, key)
}

// replace mirrors Magento\Framework\Css\PreProcessor\File\FileList\Collator::collate().
func (o *orderedFiles) replace(key string, f importFile) {
	if o.files == nil {
		o.files = map[string]importFile{}
	}
	fileID := key
	if i := strings.Index(key, "|"); i >= 0 {
		fileID = key[i:]
	}
	var keys []string
	for _, k := range o.keys {
		if strings.Contains(k, fileID) {
			delete(o.files, k)
			continue
		}
		keys = append(keys, k)
	}
	o.keys = append(keys, key)
	o.files[key] = f
}

func (o *orderedFiles) all() []importFile {
	result := make([]importFile, 0, len(o.keys))
	for _, k := range o.keys {
		result = append(result, o.files[k])
	}
	return result
}

// magentoImportFiles mirrors Magento\Framework\Css\PreProcessor\File\Collector\Aggregated::getFiles().
func (m *lessMaterializer) magentoImportFiles(filePath string) []importFile {
	p := m.p
	env := p.d.env
	ix := p.d.index
	designTheme := "|theme:" + p.Theme.FullPath()

	library := &orderedFiles{}
	for _, f := range ix.Glob(p.libDir(), filePath) {
		library.add(designTheme+"|module:|file:"+path.Base(f), importFile{})
	}
	for _, t := range p.Theme.InheritedThemes() {
		for _, f := range ix.Glob(filepath.Join(t.Path, "web"), filePath) {
			library.replace(designTheme+"|module:|file:"+path.Base(f), importFile{})
		}
	}

	list := &orderedFiles{}
	for i, k := range library.keys {
		list.add(k, library.all()[i])
	}

	type baseFile struct {
		module string
		name   string
		isBase bool
	}
	var baseFiles []baseFile
	for _, area := range []string{"base", p.Area} {
		for _, mod := range env.RegisteredModules {
			for _, f := range ix.Glob(filepath.Join(mod.Path, "view", area, "web"), filePath) {
				if !env.IsEnabled(mod.Name) {
					continue
				}
				baseFiles = append(baseFiles, baseFile{module: mod.Name, name: path.Base(f), isBase: area == "base"})
			}
		}
	}
	sort.SliceStable(baseFiles, func(i, j int) bool {
		a, b := baseFiles[i], baseFiles[j]
		if a.module == b.module {
			return a.name < b.name
		}
		pa, pb := env.ModulePriority(a.module), env.ModulePriority(b.module)
		if pa == pb {
			return a.module < b.module
		}
		return pa < pb
	})
	for _, f := range baseFiles {
		prefix := ""
		if f.isBase {
			prefix = "base"
		}
		list.add(prefix+"|module:"+f.module+"|file:"+f.name, importFile{module: f.module})
	}

	for _, t := range p.Theme.InheritedThemes() {
		for _, mod := range p.themeModules(t) {
			for _, f := range ix.Glob(filepath.Join(t.Path, mod, "web"), filePath) {
				if env.StaticContentOnlyEnabledModules && !env.IsEnabled(mod) {
					continue
				}
				list.replace("|module:"+mod+"|file:"+path.Base(f), importFile{module: mod})
			}
		}
	}
	return list.all()
}

// processImport mirrors Magento\Framework\Css\PreProcessor\Instruction\Import::process().
func (m *lessMaterializer) processImport(a asset, content string) (string, []relatedFile) {
	var related []relatedFile
	stripped := removeComments(content)
	processed := importRegex.ReplaceAllStringFunc(stripped, func(match string) string {
		sub := importRegex.FindStringSubmatch(match)
		start, filePath, end := sub[1], sub[2], sub[3]
		matchedFileID := fixFileExtension(filePath, "less")
		if start != "" && !strings.HasPrefix(strings.TrimSpace(start), "url") {
			related = append(related, relatedFile{fileID: matchedFileID, asset: a})
		}
		resolvedPath := m.convertModuleNotationToPath(a, matchedFileID)
		return "@import " + start + resolvedPath + end + ";"
	})
	if processed != stripped {
		return processed, related
	}
	return content, related
}

// convertModuleNotationToPath mirrors Magento\Framework\View\Asset\NotationResolver\Module.
func (m *lessMaterializer) convertModuleNotationToPath(a asset, relatedFileID string) string {
	if !strings.Contains(relatedFileID, "::") {
		return relatedFileID
	}
	pkgPath := m.p.Path()
	thisPath := a.Path(pkgPath)
	module, filePath, _ := strings.Cut(relatedFileID, "::")
	if module == "" {
		module = a.Module
	}
	relatedPath := asset{Module: module, FilePath: filePath}.Path(pkgPath)
	offset := offsetPath(relatedPath, thisPath)
	return normalizePath(offset + "/" + path.Base(relatedPath))
}

func fixFileExtension(fileID, contentType string) string {
	if path.Ext(path.Base(fileID)) == "" {
		return fileID + "." + contentType
	}
	return fileID
}

// normalizePath mirrors Magento\Framework\View\FileSystem::normalizePath().
func normalizePath(p string) string {
	parts := strings.Split(p, "/")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		switch {
		case part == "..":
			if len(result) == 0 || result[len(result)-1] == ".." {
				result = append(result, part)
			} else {
				result = result[:len(result)-1]
			}
		case part != ".":
			result = append(result, part)
		}
	}
	return strings.Join(result, "/")
}

// offsetPath mirrors Magento\Framework\View\FileSystem::offsetPath().
func offsetPath(relatedPath, p string) string {
	relatedPath = normalizePath(relatedPath)
	p = normalizePath(p)
	one := strings.Split(relatedPath, "/")
	two := strings.Split(p, "/")
	for len(one) > 0 && len(two) > 0 && one[0] == two[0] {
		one, two = one[1:], two[1:]
	}
	relatedPath, p = strings.Join(one, "/"), strings.Join(two, "/")
	toDir := strings.TrimLeft(phpDirname(p), "/")
	offset := ""
	if toDir != "." {
		offset = strings.Repeat("../", len(strings.Split(toDir, "/")))
	}
	return strings.TrimRight(offset+phpDirname(relatedPath), "/")
}

func phpDirname(p string) string {
	i := strings.LastIndex(p, "/")
	switch {
	case i < 0:
		return "."
	case i == 0:
		return "/"
	default:
		return p[:i]
	}
}

func isPCRESpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

// removeComments mirrors Import::removeComments():
// preg_replace("#(^\s*//.*$)|((^\s*/\*(?s).*?(\*/)(?!\*/))$)#m", ”, $content)
func removeComments(content string) string {
	var sb strings.Builder
	n := len(content)
	last := 0
	i := 0
	for i < n {
		if i != 0 && content[i-1] != '\n' {
			i++
			continue
		}
		j := i
		for j < n && isPCRESpace(content[j]) {
			j++
		}
		end := -1
		if j+1 < n && content[j] == '/' && content[j+1] == '/' {
			end = j + 2
			for end < n && content[end] != '\n' {
				end++
			}
		} else if j+1 < n && content[j] == '/' && content[j+1] == '*' {
			k := j + 2
			for {
				idx := strings.Index(content[k:], "*/")
				if idx < 0 {
					break
				}
				pos := k + idx
				after := pos + 2
				if !strings.HasPrefix(content[after:], "*/") && (after == n || content[after] == '\n') {
					end = after
					break
				}
				k = pos + 1
			}
		}
		if end < 0 {
			i++
			continue
		}
		sb.WriteString(content[last:i])
		last = end
		i = end
		if i == n {
			break
		}
	}
	sb.WriteString(content[last:])
	return sb.String()
}

// trimContent trims the content of a related file when the installed Magento version does.
func (m *lessMaterializer) trimContent(content string) string {
	if !m.p.d.env.AssetContentTrimmed {
		return content
	}
	return phpTrim(content)
}

// phpTrim mirrors PHP's trim() with the default character list.
func phpTrim(s string) string { return strings.Trim(s, " \t\n\r\x00\x0B") }

func writeFile(target string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, data, 0o644)
}
