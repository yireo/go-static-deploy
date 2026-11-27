package deploy

import (
	"fmt"
	"regexp"
	"strings"
)

// jshrink is a port of JShrink\Minifier v1.8.1 (tedivm/jshrink), the JavaScript minifier used by Magento. It only
// removes whitespace and comments: names, arguments and syntax are left untouched, so the output matches
// bin/magento setup:static-content:deploy. Characters are PHP style byte strings; "" stands for PHP's false.
type jshrink struct {
	input    string
	index    int
	a, b     string
	c        string
	cSet     bool
	lastChar string
	output   []byte
}

var (
	jshrinkLockRe     = regexp.MustCompile(`([+-])([ \t\n\v\f\r]+)([+-])`)
	jshrinkKeywordRes = func() []*regexp.Regexp {
		var res []*regexp.Regexp
		for _, k := range []string{"delete", "do", "for", "in", "instanceof", "return", "typeof", "yield"} {
			res = append(res, regexp.MustCompile(`(?i)[^\w]`+k+` ?\n?\z`))
		}
		return res
	}()
	jshrinkNoNewLine = map[string]bool{"(": true, "-": true, "+": true, "[": true, "#": true, "@": true}
)

const jshrinkMaxKeywordLen = len("instanceof")

type jshrinkError struct{ msg string }

func (e *jshrinkError) Error() string { return e.msg }

// jshrinkMinify mirrors JShrink\Minifier::minify() with the default options (flagged comments preserved).
func jshrinkMinify(js string) (result string, err error) {
	defer func() {
		if r := recover(); r != nil {
			e, ok := r.(*jshrinkError)
			if !ok {
				panic(r)
			}
			err = e
		}
	}()
	js, lock, ws := jshrinkLock(js)
	m := &jshrink{input: js + "\n", a: "\n", b: "\n", lastChar: "\n"}
	m.loop()
	out := strings.TrimLeft(string(m.output), " \t\n\r\x00\x0B")
	if lock != "" {
		out = strings.ReplaceAll(out, lock, ws)
	}
	return out, nil
}

// jshrinkLock protects things like `"asd" + ++x`. As in JShrink all matches get the whitespace of the first match.
func jshrinkLock(js string) (string, string, string) {
	m := jshrinkLockRe.FindStringSubmatch(js)
	if m == nil {
		return js, "", ""
	}
	lock := `"LOCK---1f8c5e3a"`
	return jshrinkLockRe.ReplaceAllString(js, "${1}"+lock+"${3}"), lock, m[2]
}

func (m *jshrink) fail(format string, args ...any) {
	panic(&jshrinkError{fmt.Sprintf(format, args...)})
}

func (m *jshrink) echo(s string) {
	m.output = append(m.output, s...)
	if s == "" {
		m.lastChar = ""
		return
	}
	m.lastChar = s[len(s)-1:]
}

func jshrinkNormalize(char string) string {
	if char == "\r" {
		char = "\n"
	}
	if char != "\n" && char < "\x20" {
		return " "
	}
	return char
}

func (m *jshrink) getChar() string {
	var char string
	if m.cSet {
		char = m.c
		m.c, m.cSet = "", false
	} else {
		if m.index >= len(m.input) {
			return ""
		}
		char = m.input[m.index : m.index+1]
		m.index++
	}
	return jshrinkNormalize(char)
}

func (m *jshrink) peek() string {
	if m.index >= len(m.input) {
		return ""
	}
	return jshrinkNormalize(m.input[m.index : m.index+1])
}

func (m *jshrink) inputAt(i int) string {
	if i < len(m.input) {
		return m.input[i : i+1]
	}
	return ""
}

func (m *jshrink) loop() {
	for m.a != "" {
		if !m.step() {
			continue
		}
		m.b = m.getReal()
		if m.b == "/" {
			last := m.a
			if last == " " {
				last = m.lastChar
			}
			if strings.Contains("(,=:[!&|?\n", last) || m.endsInKeyword() {
				m.saveRegex()
			}
		}
	}
}

// step handles the switch block of JShrink's loop(). It returns false for JShrink's `continue 3`.
func (m *jshrink) step() bool {
	switch m.a {
	case "\r", "\n":
		if m.b != "" && jshrinkNoNewLine[m.b] {
			m.echo(m.a)
			m.saveString()
			return true
		}
		if m.b == " " {
			return true
		}
		m.space()
	case " ":
		m.space()
	default:
		switch m.b {
		case "\r", "\n":
			if strings.Contains("}])+-\"'", m.a) || isJShrinkAlphaNumeric(m.a) {
				m.echo(m.a)
				m.saveString()
			}
		case " ":
			if !isJShrinkAlphaNumeric(m.a) {
				return true
			}
			return m.other()
		default:
			return m.other()
		}
	}
	return true
}

func (m *jshrink) space() {
	if isJShrinkAlphaNumeric(m.b) {
		m.echo(m.a)
	}
	m.saveString()
}

func (m *jshrink) other() bool {
	if m.a == "/" && (m.b == "'" || m.b == `"`) {
		m.saveRegex()
		return false
	}
	m.echo(m.a)
	m.saveString()
	return true
}

func (m *jshrink) getReal() string {
	start := m.index
	char := m.getChar()
	if char != "/" {
		return char
	}
	m.c, m.cSet = m.getChar(), true
	switch m.c {
	case "/":
		m.processOneLineComments(start)
		return m.getReal()
	case "*":
		m.processMultiLineComments(start)
		return m.getReal()
	}
	return char
}

func (m *jshrink) processOneLineComments(start int) {
	third := m.inputAt(m.index)
	m.getNext("\n")
	m.c, m.cSet = "", false
	if third == "@" {
		m.c, m.cSet = "\n"+m.input[start:m.index], true
	}
}

func (m *jshrink) processMultiLineComments(start int) {
	m.getChar()
	third := m.getChar()
	if third == "*" && m.peek() == "/" {
		m.index++
		return
	}
	var char string
	if m.getNext("*/") != "" {
		m.getChar()
		m.getChar()
		char = m.getChar()
		if third == "!" || third == "@" {
			if start > 0 {
				m.echo(m.a)
				m.a = " "
				if m.input[start-1] == '\n' {
					m.echo("\n")
				}
			}
			m.echo(m.input[start : m.index-1])
			m.c, m.cSet = char, true
			return
		}
	}
	if char == "" {
		m.fail("Unclosed multiline comment at position: %d", m.index-2)
	}
	m.c, m.cSet = char, true
}

func (m *jshrink) getNext(s string) string {
	pos := strings.Index(m.input[min(m.index, len(m.input)):], s)
	if pos < 0 {
		return ""
	}
	m.index += pos
	return m.inputAt(m.index)
}

func (m *jshrink) saveString() {
	start := m.index
	m.a = m.b
	if m.a != "'" && m.a != `"` && m.a != "`" {
		return
	}
	stringType := m.a
	m.echo(m.a)
	for m.a = m.getChar(); m.a != ""; m.a = m.getChar() {
		switch m.a {
		case stringType:
			return
		case "\n":
			if stringType != "`" {
				m.fail("Unclosed string at position: %d", start)
			}
			m.echo(m.a)
		case `\`:
			m.b = m.getChar()
			if m.b != "\n" {
				m.echo(m.a + m.b)
			}
		default:
			m.echo(m.a)
		}
	}
}

func (m *jshrink) saveRegex() {
	if m.a != " " {
		m.echo(m.a)
	}
	m.echo(m.b)
	characterClass := false
	characterClassIndex := 0
	for m.a = m.getChar(); m.a != ""; m.a = m.getChar() {
		if m.a == "/" && !characterClass {
			break
		}
		if m.a == "[" {
			characterClass, characterClassIndex = true, m.index
		} else if m.a == "]" {
			characterClass = false
		}
		if m.a == `\` {
			m.echo(m.a)
			m.a = m.getChar()
		}
		if m.a == "\n" {
			if characterClass {
				m.fail("Unclosed character class at position: %d", characterClassIndex)
			}
			m.fail("Unclosed regex pattern at position: %d", m.index)
		}
		m.echo(m.a)
	}
	m.b = m.getReal()
}

// isJShrinkAlphaNumeric mirrors preg_match('/^[\w\$\pL]$/', $char) without the u modifier (so bytes are
// Latin-1 code points) or $char == '/'.
func isJShrinkAlphaNumeric(char string) bool {
	if char == "/" {
		return true
	}
	if len(char) == 2 && char[1] == '\n' {
		char = char[:1]
	}
	if len(char) != 1 {
		return false
	}
	c := char[0]
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '$':
		return true
	case c == 0xAA, c == 0xB5, c == 0xBA:
		return true
	case c >= 0xC0 && c != 0xD7 && c != 0xF7:
		return true
	}
	return false
}

func (m *jshrink) endsInKeyword() bool {
	n := jshrinkMaxKeywordLen + 10
	out := m.output
	if len(out) > n {
		out = out[len(out)-n:]
	}
	test := string(out) + m.a
	if len(test) > n {
		test = test[len(test)-n:]
	}
	for _, re := range jshrinkKeywordRes {
		if re.MatchString(test) {
			return true
		}
	}
	return false
}
