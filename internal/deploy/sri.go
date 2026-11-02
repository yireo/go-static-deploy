package deploy

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const sriFile = "sri-hashes.json"

// sriEnabled tells whether the installed Magento_Csp module writes sri-hashes.json during deployment.
func (d *Deployer) sriEnabled() bool {
	if !d.env.IsEnabled("Magento_Csp") {
		return false
	}
	return fileExists(filepath.Join(d.env.ModulePaths["Magento_Csp"], "Model", "Deploy", "Package", "Processor", "PostProcessor", "Integrity.php"))
}

// writeSRIHashes mirrors the Magento_Csp Integrity post processor: a sha256 hash of every
// deployed JavaScript file of the package.
func (p *Package) writeSRIHashes(outDir string) error {
	hashes := map[string]string{}
	err := filepath.WalkDir(outDir, func(f string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(f, ".js") {
			return err
		}
		data, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(outDir, f)
		sum := sha256.Sum256(data)
		hashes[p.Path()+"/"+filepath.ToSlash(rel)] = "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outDir, sriFile), phpJSONObject(hashes), 0o644)
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
