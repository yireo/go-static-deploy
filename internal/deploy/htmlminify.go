package deploy

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/dlclark/regexp2"

	"github.com/yireo/go-static-deploy/internal/magento"
)

// PHTML template minification modes (--html-minify) and minifiers (--html-minifier)
const (
	HTMLMinifyAuto  = "auto"
	HTMLMinifyOn    = "on"
	HTMLMinifyOff   = "off"
	HTMLMinifierPHP = "php"
	HTMLMinifierGo  = "go"
)

// ShouldMinifyHTML tells whether PHTML templates must be minified. In auto mode, Magento only reads the
// minified templates in production mode with dev/template/minify_html enabled. Without --with-db the
// database value is unknown, so the templates are minified anyway: a superfluous copy does no harm,
// a missing one breaks every page.
func ShouldMinifyHTML(setting, mode string, cfg *magento.Config, withDB bool) bool {
	switch setting {
	case HTMLMinifyOn:
		return true
	case HTMLMinifyOff:
		return false
	}
	if mode != "production" {
		return false
	}
	return !withDB || cfg.Flag("dev/template/minify_html")
}

// TemplateFiles returns the PHTML templates that Magento minifies during a deployment
// (Magento\Framework\App\Utility\Files::getPhtmlFiles()): view/*/templates of all registered
// modules and */_*/templates of all themes.
func TemplateFiles(env *magento.Env) []string {
	var files []string
	for _, m := range env.RegisteredModules {
		dirs, _ := filepath.Glob(filepath.Join(m.Path, "view", "*", "templates"))
		for _, dir := range dirs {
			files = append(files, phtmlFiles(dir)...)
		}
	}
	themes := make([]string, 0, len(env.Themes))
	for key := range env.Themes {
		themes = append(themes, key)
	}
	sort.Strings(themes)
	for _, key := range themes {
		dirs, _ := filepath.Glob(filepath.Join(env.Themes[key].Path, "*_*", "templates"))
		for _, dir := range dirs {
			files = append(files, phtmlFiles(dir)...)
		}
	}
	return files
}

func phtmlFiles(dir string) []string {
	var files []string
	_ = filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() && strings.HasSuffix(path, ".phtml") {
			files = append(files, path)
		}
		return nil
	})
	return files
}

// MinifiedTemplatePath returns where Magento looks for the minified version of a template:
// var/view_preprocessed/pub/static/<path relative to the Magento root>.
func MinifiedTemplatePath(root, file string) string {
	rel, err := filepath.Rel(root, file)
	if err != nil || strings.HasPrefix(rel, "..") {
		rel = strings.TrimPrefix(file, string(filepath.Separator))
	}
	return filepath.Join(root, "var", "view_preprocessed", "pub", "static", rel)
}

// MinifyTemplates minifies all PHTML templates into var/view_preprocessed/pub/static, like
// Magento\Deploy\Service\MinifyTemplates. It returns the number of minified templates.
func (d *Deployer) MinifyTemplates() (int, error) {
	root := d.env.Root
	if !d.opts.NoClean {
		for _, dir := range []string{"app", "vendor"} {
			_ = os.RemoveAll(filepath.Join(root, "var", "view_preprocessed", "pub", "static", dir))
		}
	}
	files := TemplateFiles(d.env)
	if d.opts.HTMLMinifier == HTMLMinifierGo {
		return d.minifyTemplatesGo(files)
	}
	return d.minifyTemplatesPHP(files)
}

func (d *Deployer) minifyTemplatesPHP(files []string) (int, error) {
	const chunkSize = 200
	var count atomic.Int64
	var wg sync.WaitGroup
	var errMu sync.Mutex
	var firstErr error
	sem := make(chan struct{}, d.opts.Jobs)
	for start := 0; start < len(files); start += chunkSize {
		chunk := files[start:min(start+chunkSize, len(files))]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			resp, err := d.php.Call(map[string]any{"cmd": "minify-html", "files": chunk})
			if err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
				return
			}
			count.Add(int64(resp.Count))
		}()
	}
	wg.Wait()
	return int(count.Load()), firstErr
}

func (d *Deployer) minifyTemplatesGo(files []string) (int, error) {
	var count atomic.Int64
	var wg sync.WaitGroup
	var errMu sync.Mutex
	var firstErr error
	sem := make(chan struct{}, d.opts.Jobs)
	for _, file := range files {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := minifyTemplateFile(file, MinifiedTemplatePath(d.env.Root, file)); err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
				return
			}
			count.Add(1)
		}()
	}
	wg.Wait()
	return int(count.Load()), firstErr
}

func minifyTemplateFile(src, dst string) error {
	content, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	minified, err := MinifyHTMLTemplate(content)
	if err != nil {
		return fmt.Errorf("minifying %s: %w", src, err)
	}
	return os.WriteFile(dst, minified, 0o644)
}

// The regular expressions of Magento\Framework\View\Template\Html\Minifier::minify(), translated to
// .NET syntax (regexp2): possessive quantifiers became atomic groups and \s became PCRE's ASCII-only
// whitespace class, because the content is matched byte by byte (PHP without the /u modifier).
const (
	pcreSpace    = `[ \t\n\x0B\f\r]`
	pcreNonSpace = `[^ \t\n\x0B\f\r]`
)

var inlineHTMLTags = []string{
	"b", "big", "i", "small", "tt", "abbr", "acronym", "cite", "code", "dfn", "em", "kbd", "strong", "samp",
	"var", "a", "bdo", "br", "img", "map", "object", "q", "span", "sub", "sup", "button", "input", "label",
	"select", "textarea", `\?`,
}

type htmlReplacement struct {
	re   *regexp2.Regexp
	repl string
}

var (
	htmlMinifyOnce   sync.Once
	heredocRe        *regexp2.Regexp
	heredocRestoreRe *regexp2.Regexp
	htmlReplacements []htmlReplacement
)

func compileHTMLMinifier() {
	s := func(pattern string) string {
		pattern = strings.ReplaceAll(pattern, `\S`, pcreNonSpace)
		return strings.ReplaceAll(pattern, `\s`, pcreSpace)
	}
	mustCompile := func(pattern string) *regexp2.Regexp {
		return regexp2.MustCompile(pattern, regexp2.None)
	}
	heredocRe = mustCompile(`(?is)<<<([A-z]+).*?\1;`)
	heredocRestoreRe = mustCompile(`(?is)__MINIFIED_HEREDOC__(\d+)`)
	// Innermost preg_replace() of Magento first
	htmlReplacements = []htmlReplacement{
		{mustCompile(s(`(?<!:)//[^\n\r(?>)]*(<\?php)[^\n\r]*(\s\?>)[^\n\r]*`)), ``},
		{mustCompile(`(?<!:|'|")//[^\n\r<]*(\?>)`), ` $1`},
		{mustCompile(s(`(?<!:|\\|'|"|/)//(?!/)(?!\s*<!\[)(?!\s*]]>)[^\n\r]*`)), ``},
		{mustCompile(`(?i)(?>[\t\n\x0B\f\r]` + pcreSpace + `*|` + pcreSpace + `{2,})` +
			`(?=(?>(?:(?>[^<]+)|<(?!/?(?:textarea|pre|script)\b))*)(?:<(?>textarea|pre|script)\b|\z))`), ` `},
		{mustCompile(`(?<!` + strings.Join(inlineHTMLTags, "|") + `)> <`), `><`},
		{mustCompile(s(`((?:<\?php\s+(?!echo|print|if|elseif|else)[^\?]*)\?>)\s+`)), `$1 `},
		{mustCompile(s(`(?<!]]>)\s+</`)), `</`},
	}
}

// MinifyHTMLTemplate is a port of Magento\Framework\View\Template\Html\Minifier::minify().
func MinifyHTMLTemplate(content []byte) ([]byte, error) {
	htmlMinifyOnce.Do(compileHTMLMinifier)
	text := bytesToLatin1(content)

	var heredocs []string
	text, err := heredocRe.ReplaceFunc(text, func(m regexp2.Match) string {
		heredocs = append(heredocs, m.String())
		return "__MINIFIED_HEREDOC__" + strconv.Itoa(len(heredocs)-1)
	}, -1, -1)
	if err != nil {
		return nil, err
	}
	for _, r := range htmlReplacements {
		if text, err = r.re.Replace(text, r.repl, -1, -1); err != nil {
			return nil, err
		}
	}
	text, err = heredocRestoreRe.ReplaceFunc(text, func(m regexp2.Match) string {
		i, _ := strconv.Atoi(m.GroupByNumber(1).String())
		if i < len(heredocs) {
			return heredocs[i]
		}
		return ""
	}, -1, -1)
	if err != nil {
		return nil, err
	}
	return latin1ToBytes(strings.TrimRight(text, " \t\n\r\x00\x0B")), nil
}

// bytesToLatin1 maps every byte to one rune, so the regular expressions work on bytes like PCRE without /u.
func bytesToLatin1(b []byte) string {
	runes := make([]rune, len(b))
	for i, c := range b {
		runes[i] = rune(c)
	}
	return string(runes)
}

func latin1ToBytes(s string) []byte {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		out = append(out, byte(r))
	}
	return out
}
