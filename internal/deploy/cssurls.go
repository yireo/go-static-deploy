package deploy

import (
	"path"
	"regexp"
	"strings"
)

// Placeholders like {{base_url_path}} in CSS URLs: /{{([_a-z]*)}}/si (ASCII only, like PCRE
// without the u modifier)
var cssVariableRegex = regexp.MustCompile(`\{\{([_a-zA-Z]*)\}\}`)

// processCSS mirrors the VariableNotation and ModuleNotation CSS pre-processors of Magento.
func processCSS(content, area, themePath, contextPath, assetPath string) string {
	content = replaceCSSVariables(content, area, themePath)
	return replaceCSSModuleNotation(content, contextPath, assetPath)
}

// replaceCSSVariables mirrors Magento\Framework\View\Asset\PreProcessor\VariableNotation.
func replaceCSSVariables(content, area, themePath string) string {
	return replaceRelativeUrls(content, func(p string) string {
		matches := cssVariableRegex.FindAllStringSubmatch(p, -1)
		if len(matches) == 0 {
			return p
		}
		var keys []string
		replacements := map[string]string{}
		for _, m := range matches {
			if _, ok := replacements[m[0]]; ok {
				continue
			}
			value := ""
			if m[1] == "base_url_path" {
				value = "{{base_url_path}}" + area
				if themePath != "" && themePath != "0" {
					value += "/" + themePath + "/"
				}
				value += "{{locale}}"
			}
			keys = append(keys, m[0])
			replacements[m[0]] = value
		}
		for _, k := range keys {
			p = strings.ReplaceAll(p, k, replacements[k])
		}
		return p
	})
}

// replaceCSSModuleNotation mirrors Magento\Framework\View\Asset\PreProcessor\ModuleNotation.
func replaceCSSModuleNotation(content, contextPath, assetPath string) string {
	thisPath := contextPath + "/" + assetPath
	return replaceRelativeUrls(content, func(p string) string {
		module, filePath, ok := strings.Cut(p, "::")
		if !ok {
			return p
		}
		relatedPath := contextPath + "/" + module + "/" + filePath
		offset := offsetPath(relatedPath, thisPath)
		return normalizePath(offset + "/" + path.Base(relatedPath))
	})
}

// replaceRelativeUrls mirrors Magento\Framework\View\Url\CssResolver::replaceRelativeUrls().
func replaceRelativeUrls(content string, callback func(string) string) string {
	patterns, paths := extractRelativeUrls(content)
	for _, pattern := range patterns {
		p := paths[pattern]
		content = strings.ReplaceAll(content, pattern, strings.ReplaceAll(pattern, p, callback(p)))
	}
	return content
}

// extractRelativeUrls mirrors CssResolver::extractRelativeUrls(): array_combine() of all
// matches of CssResolver::REGEX_CSS_RELATIVE_URLS. It returns the unique full matches in
// order of appearance, and the path (first group) per full match.
func extractRelativeUrls(content string) ([]string, map[string]string) {
	var patterns []string
	paths := map[string]string{}
	for i := 0; i < len(content); {
		idx := strings.Index(content[i:], "url")
		if idx < 0 {
			break
		}
		start := i + idx
		end, gStart, gEnd, ok := matchRelativeURL(content, start)
		if !ok {
			i = start + 1
			continue
		}
		pattern := content[start:end]
		if _, seen := paths[pattern]; !seen {
			patterns = append(patterns, pattern)
		}
		paths[pattern] = content[gStart:gEnd]
		i = end
	}
	return patterns, paths
}

// matchRelativeURL matches CssResolver::REGEX_CSS_RELATIVE_URLS at position i, with the
// backtracking order of PCRE:
//
//	#url\s*\(\s*(?(?=\'|").)(?!http\://|https\://|/|data\:)(.+?)(?:[\#\?].*?|[\'"])?\s*\)#
//
// It returns the end of the match and the bounds of the first group.
func matchRelativeURL(s string, i int) (end, gStart, gEnd int, ok bool) {
	if !strings.HasPrefix(s[i:], "url") {
		return 0, 0, 0, false
	}
	p := skipPCRESpace(s, i+3)
	if p >= len(s) || s[p] != '(' {
		return 0, 0, 0, false
	}
	p++
	for q := skipPCRESpace(s, p); q >= p; q-- {
		start := q
		if start < len(s) && (s[start] == '\'' || s[start] == '"') {
			start++
		}
		rest := s[start:]
		if strings.HasPrefix(rest, "http://") || strings.HasPrefix(rest, "https://") ||
			strings.HasPrefix(rest, "/") || strings.HasPrefix(rest, "data:") {
			continue
		}
		for g := start + 1; g <= len(s); g++ {
			if s[g-1] == '\n' {
				break
			}
			if e, ok := matchURLTail(s, g); ok {
				return e, start, g, true
			}
		}
	}
	return 0, 0, 0, false
}

// matchURLTail matches (?:[\#\?].*?|[\'"])?\s*\) at position q.
func matchURLTail(s string, q int) (int, bool) {
	if q < len(s) && (s[q] == '#' || s[q] == '?') {
		for k := q + 1; ; k++ {
			if e, ok := matchCloseParen(s, k); ok {
				return e, true
			}
			if k >= len(s) || s[k] == '\n' {
				break
			}
		}
	}
	if q < len(s) && (s[q] == '\'' || s[q] == '"') {
		if e, ok := matchCloseParen(s, q+1); ok {
			return e, true
		}
	}
	return matchCloseParen(s, q)
}

// matchCloseParen matches \s*\) at position k.
func matchCloseParen(s string, k int) (int, bool) {
	k = skipPCRESpace(s, k)
	if k < len(s) && s[k] == ')' {
		return k + 1, true
	}
	return 0, false
}

func skipPCRESpace(s string, i int) int {
	for i < len(s) && isPCRESpace(s[i]) {
		i++
	}
	return i
}
