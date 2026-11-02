package deploy

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Index caches recursive directory listings. Like Magento's glob based file
// collection it skips entries starting with a dot and follows symlinks.
type Index struct {
	mu   sync.Mutex
	dirs map[string]*dirEntry
}

type dirEntry struct {
	once    sync.Once
	files   []string
	set     map[string]struct{}
	subdirs []string
}

// NewIndex creates an empty index.
func NewIndex() *Index { return &Index{dirs: map[string]*dirEntry{}} }

func (ix *Index) get(dir string) *dirEntry {
	ix.mu.Lock()
	e, ok := ix.dirs[dir]
	if !ok {
		e = &dirEntry{}
		ix.dirs[dir] = e
	}
	ix.mu.Unlock()
	e.once.Do(func() {
		e.set = map[string]struct{}{}
		walk(dir, "", e, 0)
		sort.Strings(e.files)
		sort.Strings(e.subdirs)
	})
	return e
}

func walk(base, rel string, e *dirEntry, depth int) {
	if depth > 64 {
		return
	}
	entries, err := os.ReadDir(filepath.Join(base, rel))
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		childRel := name
		if rel != "" {
			childRel = rel + "/" + name
		}
		isDir := entry.IsDir()
		if entry.Type()&os.ModeSymlink != 0 {
			info, err := os.Stat(filepath.Join(base, childRel))
			if err != nil {
				continue
			}
			isDir = info.IsDir()
		}
		if isDir {
			if rel == "" {
				e.subdirs = append(e.subdirs, name)
			}
			walk(base, childRel, e, depth+1)
			continue
		}
		e.files = append(e.files, childRel)
		e.set[childRel] = struct{}{}
	}
}

// List returns all files below dir as slash separated relative paths, sorted.
func (ix *Index) List(dir string) []string { return ix.get(dir).files }

// Has tells whether dir/rel is a file.
func (ix *Index) Has(dir, rel string) bool {
	if strings.HasPrefix(rel, "../") || strings.Contains(rel, "/../") {
		info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
		return err == nil && !info.IsDir()
	}
	_, ok := ix.get(dir).set[rel]
	return ok
}

// Subdirs returns the direct sub directories of dir, sorted.
func (ix *Index) Subdirs(dir string) []string { return ix.get(dir).subdirs }

// Glob returns the files below dir matching a glob pattern (relative, slash separated).
func (ix *Index) Glob(dir, pattern string) []string {
	if !strings.ContainsAny(pattern, "*?[{") {
		if ix.Has(dir, pattern) {
			return []string{pattern}
		}
		return nil
	}
	var result []string
	for _, f := range ix.List(dir) {
		if ok, _ := path.Match(pattern, f); ok {
			result = append(result, f)
		}
	}
	return result
}
