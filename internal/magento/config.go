package magento

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Config holds the merged default-scope system configuration:
// module etc/config.xml defaults < database < app/etc/config.php < app/etc/env.php.
type Config struct {
	values map[string]string
	order  []string
}

// NewConfig creates an empty configuration.
func NewConfig() *Config { return &Config{values: map[string]string{}} }

func (c *Config) set(path, value string) {
	if _, ok := c.values[path]; !ok {
		c.order = append(c.order, path)
	}
	c.values[path] = value
}

// Get returns the value of a configuration path.
func (c *Config) Get(path string) string { return c.values[path] }

// Flag returns true for a configuration flag that is set (Magento isSetFlag()).
func (c *Config) Flag(path string) bool {
	v := strings.ToLower(strings.TrimSpace(c.values[path]))
	return v != "" && v != "0" && v != "false"
}

// Set overrides a value (used for command line overrides and tests).
func (c *Config) Set(path, value string) { c.set(path, value) }

// List returns the values below a configuration node, for array-like nodes such as
// dev/js/minify_exclude. A scalar value is split on newlines (legacy format).
func (c *Config) List(path string) []string {
	var result []string
	prefix := path + "/"
	for _, p := range c.order {
		if strings.HasPrefix(p, prefix) && !strings.Contains(p[len(prefix):], "/") {
			result = append(result, c.values[p])
		}
	}
	if len(result) == 0 {
		if v, ok := c.values[path]; ok {
			for _, line := range strings.Split(v, "\n") {
				if strings.TrimSpace(line) != "" {
					result = append(result, strings.TrimSpace(line))
				}
			}
		}
	}
	return result
}

func loadConfig(env *Env, systemDefault, dbDefault map[string]any) (*Config, error) {
	cfg := NewConfig()
	for _, module := range env.EnabledModules {
		file := filepath.Join(env.ModulePaths[module], "etc", "config.xml")
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		if err := parseConfigXML(data, cfg); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", file, err)
		}
	}
	for _, src := range []map[string]any{dbDefault, systemDefault} {
		for path, v := range src {
			if v == nil {
				cfg.set(path, "")
				continue
			}
			cfg.set(path, fmt.Sprint(v))
		}
	}
	return cfg, nil
}

// parseConfigXML flattens the <default> section of a config.xml file.
func parseConfigXML(data []byte, cfg *Config) error {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var stack []string
	var text strings.Builder
	var hasChild []bool
	inDefault := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if len(stack) == 1 && stack[0] == "config" && t.Name.Local == "default" {
				inDefault = true
			}
			if len(hasChild) > 0 {
				hasChild[len(hasChild)-1] = true
			}
			stack = append(stack, t.Name.Local)
			hasChild = append(hasChild, false)
			text.Reset()
		case xml.CharData:
			text.Write(t)
		case xml.EndElement:
			if inDefault && len(stack) > 2 && !hasChild[len(hasChild)-1] {
				cfg.set(strings.Join(stack[2:], "/"), strings.TrimSpace(text.String()))
			}
			if len(stack) == 2 && stack[1] == "default" {
				inDefault = false
			}
			stack = stack[:len(stack)-1]
			hasChild = hasChild[:len(hasChild)-1]
			text.Reset()
		}
	}
}
