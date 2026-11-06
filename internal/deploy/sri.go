package deploy

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const sriFile = "sri-hashes.json"

// sriAreas are the areas whose sri-hashes.json Magento removes when a deployment starts.
var sriAreas = []string{"base", "frontend", "adminhtml"}

// sriEnabled tells whether the installed Magento_Csp module writes sri-hashes.json during deployment.
func (d *Deployer) sriEnabled() bool {
	if d.opts.NoSRI || d.opts.NoJavascript || !d.env.IsEnabled("Magento_Csp") {
		return false
	}
	return fileExists(filepath.Join(d.env.ModulePaths["Magento_Csp"], "Model", "Deploy", "Package", "Processor", "PostProcessor", "Integrity.php"))
}

// SRI layouts of the installed Magento_Csp module
const (
	// sriLayoutPackage: sri-hashes.json in every package, with the hashes of all deployed JavaScript files
	// (older Magento versions, without the SubresourceIntegrity storage)
	sriLayoutPackage = "package"
	// sriLayoutArea: pub/static/<area>/sri-hashes.json, with the hashes of the source files (Magento 2.4.6 - 2.4.8)
	sriLayoutArea = "area"
	// sriLayoutPackageDeployed: sri-hashes.json in every package, written by the SubresourceIntegrity storage,
	// with the hashes of the deployed JavaScript files of the package (Magento 2.4.9 and Mage-OS 3 and later)
	sriLayoutPackageDeployed = "package-deployed"
)

// sriLayout detects where and how Magento_Csp stores the hashes during deployment.
func (d *Deployer) sriLayout() string {
	d.sriLayoutOnce.Do(func() {
		csp := d.env.ModulePaths["Magento_Csp"]
		d.sriLayoutValue = sriLayoutPackage
		if !fileExists(filepath.Join(csp, "Model", "SubresourceIntegrity", "Storage", "File.php")) {
			return
		}
		d.sriLayoutValue = sriLayoutPackageDeployed
		integrity, err := os.ReadFile(filepath.Join(csp, "Model", "Deploy", "Package", "Processor", "PostProcessor", "Integrity.php"))
		if err == nil && sriAreaContext.Match(integrity) {
			d.sriLayoutValue = sriLayoutArea
		}
	})
	return d.sriLayoutValue
}

// sriAreaContext matches the Integrity post processor of Magento 2.4.6 - 2.4.8, which saves the hashes with the area
// as context: $area = explode('/', $package->getPath())[0];
var sriAreaContext = regexp.MustCompile(`explode\(\s*'/'\s*,\s*\$package->getPath\(\)\s*\)\s*\[\s*0\s*\]`)

// sriPerArea tells whether Magento_Csp stores the hashes per area in pub/static/<area>/sri-hashes.json.
func (d *Deployer) sriPerArea() bool {
	return d.sriLayout() == sriLayoutArea
}

// removeSRIHashes mirrors the Magento_Csp RemoveAllAssetIntegrityHashes plugin: the hashes of all areas are
// removed when a deployment starts.
func (d *Deployer) removeSRIHashes() {
	for _, area := range sriAreas {
		_ = os.Remove(filepath.Join(d.env.Root, "pub", "static", area, sriFile))
	}
}

// WriteSRIHashes writes pub/static/<area>/sri-hashes.json with the hashes collected by Run, merged with the
// hashes already in the file (for instance of themes deployed by bin/magento with --luma-dispatch).
func (d *Deployer) WriteSRIHashes() error {
	d.sriMu.Lock()
	defer d.sriMu.Unlock()
	for area, hashes := range d.sriHashes {
		file := filepath.Join(d.env.Root, "pub", "static", area, sriFile)
		merged := map[string]string{}
		if data, err := os.ReadFile(file); err == nil {
			_ = json.Unmarshal(data, &merged)
		}
		for k, v := range hashes {
			merged[k] = v
		}
		if err := writeFile(file, phpJSONObject(merged)); err != nil {
			return err
		}
	}
	return nil
}

// writeSRIHashes mirrors the Magento_Csp Integrity post processor for a deployed package.
func (p *Package) writeSRIHashes(outDir string, entries []entry) error {
	hashes, err := p.deployedJSHashes(outDir)
	if err != nil {
		return err
	}
	switch p.d.sriLayout() {
	case sriLayoutPackage:
		// Older Magento_Csp versions: a sha256 hash of every deployed JavaScript file of the package
		return os.WriteFile(filepath.Join(outDir, sriFile), phpJSONObject(hashes), 0o644)
	case sriLayoutPackageDeployed:
		// The Integrity post processor hashes the deployed (minified) file of every JavaScript file of the
		// package. requirejs-min-resolver.js is created after the post processors.
		delete(hashes, p.Path()+"/"+requireJsMinResolver)
		delete(hashes, p.Path()+"/"+p.d.minifiedName(p.Path(), requireJsMinResolver))
		return os.WriteFile(filepath.Join(outDir, sriFile), phpJSONObject(hashes), 0o644)
	}

	// The Integrity post processor hashes the source file of every JavaScript file of the package, under its
	// name without minification sign. Generated files (RequireJS config, bundles) are hashed as deployed,
	// except requirejs-min-resolver.js, which Magento creates after the post processors.
	for _, e := range entries {
		if e.kind != kindJS {
			continue
		}
		src := p.Resolve(e.module, e.name)
		if src == "" {
			src = e.source
		}
		sum, err := fileHash(src)
		if err != nil {
			return err
		}
		// file.min.js stays listed (with the deployed hash) when it is a source file of its own
		if e.target != e.rel() && p.Resolve(e.module, strings.TrimSuffix(e.name, ".js")+".min.js") == "" {
			delete(hashes, p.Path()+"/"+e.target)
		}
		hashes[p.Path()+"/"+e.rel()] = sum
	}
	delete(hashes, p.Path()+"/"+requireJsMinResolver)
	delete(hashes, p.Path()+"/"+p.d.minifiedName(p.Path(), requireJsMinResolver))

	p.d.sriMu.Lock()
	defer p.d.sriMu.Unlock()
	if p.d.sriHashes[p.Area] == nil {
		p.d.sriHashes[p.Area] = map[string]string{}
	}
	for k, v := range hashes {
		p.d.sriHashes[p.Area][k] = v
	}
	return nil
}

// deployedJSHashes returns the sha256 hash of every deployed JavaScript file of the package.
func (p *Package) deployedJSHashes(outDir string) (map[string]string, error) {
	hashes := map[string]string{}
	err := filepath.WalkDir(outDir, func(f string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(f, ".js") {
			return err
		}
		sum, err := fileHash(f)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(outDir, f)
		hashes[p.Path()+"/"+filepath.ToSlash(rel)] = sum
		return nil
	})
	return hashes, err
}

func fileHash(f string) (string, error) {
	data, err := os.ReadFile(f)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256-" + base64.StdEncoding.EncodeToString(sum[:]), nil
}

// phpJSONObject encodes a string map like PHP's json_encode() with default flags.
func phpJSONObject(m map[string]string) []byte {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	sb.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte(',')
		}
		phpJSONString(&sb, k)
		sb.WriteByte(':')
		phpJSONString(&sb, m[k])
	}
	sb.WriteByte('}')
	return []byte(sb.String())
}

func phpJSONString(sb *strings.Builder, s string) {
	sb.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			sb.WriteString(`\"`)
		case r == '\\':
			sb.WriteString(`\\`)
		case r == '/':
			sb.WriteString(`\/`)
		case r == '\n':
			sb.WriteString(`\n`)
		case r == '\r':
			sb.WriteString(`\r`)
		case r == '\t':
			sb.WriteString(`\t`)
		case r == '\b':
			sb.WriteString(`\b`)
		case r == '\f':
			sb.WriteString(`\f`)
		case r < 0x20 || r == utf8.RuneError:
			fmt.Fprintf(sb, `\u%04x`, r)
		case r < 0x80:
			sb.WriteRune(r)
		case r < 0x10000:
			fmt.Fprintf(sb, `\u%04x`, r)
		default:
			r -= 0x10000
			fmt.Fprintf(sb, `\u%04x\u%04x`, 0xD800+(r>>10), 0xDC00+(r&0x3FF))
		}
	}
	sb.WriteByte('"')
}
