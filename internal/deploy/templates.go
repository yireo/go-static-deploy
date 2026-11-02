package deploy

import (
	"path/filepath"
	"strings"
	"sync"
)

// templateFiles mirrors Magento\Framework\App\Utility\Files::getPhtmlFiles(false, false).
func (d *Deployer) templateFiles() []string {
	var files []string
	seen := map[string]bool{}
	add := func(dir string) {
		if seen[dir] {
			return
		}
		seen[dir] = true
		for _, f := range d.index.List(dir) {
			if strings.HasSuffix(f, ".phtml") {
				files = append(files, filepath.Join(dir, filepath.FromSlash(f)))
			}
		}
	}
	for _, m := range d.env.RegisteredModules {
		viewDir := filepath.Join(m.Path, "view")
		for _, area := range d.index.Subdirs(viewDir) {
			add(filepath.Join(viewDir, area, "templates"))
		}
	}
	for _, area := range d.env.Areas() {
		for _, t := range d.env.ThemesForArea(area) {
			for _, sub := range d.index.Subdirs(t.Path) {
				if strings.Contains(sub, "_") {
					add(filepath.Join(t.Path, sub, "templates"))
				}
			}
		}
	}
	return files
}

// MinifyTemplates mirrors Magento\Deploy\Service\MinifyTemplates using Magento's own
// HTML minifier through the PHP workers.
func (d *Deployer) MinifyTemplates() (int, error) {
	files := d.templateFiles()
	const chunkSize = 100
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	count := 0
	for i := 0; i < len(files); i += chunkSize {
		chunk := files[i:min(i+chunkSize, len(files))]
		wg.Add(1)
		go func(chunk []string) {
			defer wg.Done()
			resp, err := d.php.Call(map[string]any{"cmd": "minify-templates", "files": chunk})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			count += resp.Count
		}(chunk)
	}
	wg.Wait()
	return count, firstErr
}
