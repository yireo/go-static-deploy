package deploy

import (
	"bytes"
	"path"
	"regexp"
	"strings"

	"github.com/tdewolff/minify/v2"
	"github.com/tdewolff/minify/v2/css"
	"github.com/tdewolff/minify/v2/js"
)

// minification mirrors Magento\Framework\View\Asset\Minification.
type minification struct {
	enabled  map[string]bool
	excludes map[string][]*regexp.Regexp
	raw      map[string][]string
	m        *minify.M
}

func newMinification(enabledJS, enabledCSS bool, excludeJS, excludeCSS []string) *minification {
	mf := &minification{
		enabled:  map[string]bool{"js": enabledJS, "css": enabledCSS},
		excludes: map[string][]*regexp.Regexp{},
		raw:      map[string][]string{"js": excludeJS, "css": excludeCSS},
		m:        minify.New(),
	}
	for ext, list := range mf.raw {
		for _, expr := range list {
			expr = strings.TrimSpace(expr)
			if expr == "" {
				continue
			}
			re, err := regexp.Compile(expr)
			if err != nil {
				re = regexp.MustCompile(regexp.QuoteMeta(expr))
			}
			mf.excludes[ext] = append(mf.excludes[ext], re)
		}
	}
	mf.m.AddFunc("text/css", css.Minify)
	mf.m.AddFunc("application/javascript", js.Minify)
	return mf
}

func isMinifiedFilename(name string) bool {
	dot := strings.LastIndex(name, ".")
	return dot >= 4 && name[dot-4:dot+1] == ".min."
}

func (mf *minification) isEnabled(ext string) bool { return mf.enabled[ext] }

func (mf *minification) isExcluded(fullPath string) bool {
	for _, re := range mf.excludes[strings.TrimPrefix(path.Ext(fullPath), ".")] {
		if re.MatchString(fullPath) {
			return true
		}
	}
	return false
}

// addMinifiedSign turns file.js into file.min.js when minification applies.
func (mf *minification) addMinifiedSign(fullPath string) string {
	ext := strings.TrimPrefix(path.Ext(fullPath), ".")
	if !mf.isEnabled(ext) || mf.isExcluded(fullPath) || isMinifiedFilename(fullPath) {
		return fullPath
	}
	return fullPath[:len(fullPath)-len(ext)] + "min." + ext
}

// excludesFor returns the raw exclude expressions (used for the RequireJS min resolver).
func (mf *minification) excludesFor(ext string) []string {
	var result []string
	for _, e := range mf.raw[ext] {
		if strings.TrimSpace(e) != "" {
			result = append(result, strings.TrimSpace(e))
		}
	}
	return result
}

func (mf *minification) minifyJS(content []byte) []byte {
	var buf bytes.Buffer
	if err := mf.m.Minify("application/javascript", &buf, bytes.NewReader(content)); err != nil {
		return content
	}
	return buf.Bytes()
}

func (mf *minification) minifyCSS(content []byte) []byte {
	var buf bytes.Buffer
	if err := mf.m.Minify("text/css", &buf, bytes.NewReader(content)); err != nil {
		return content
	}
	return buf.Bytes()
}

// minifiedName returns the deployed name of a package relative file honouring minification.
func (d *Deployer) minifiedName(pkgPath, rel string) string {
	full := d.minify.addMinifiedSign(pkgPath + "/" + rel)
	return strings.TrimPrefix(full, pkgPath+"/")
}
