package magento

import "testing"

func TestParseConfigXML(t *testing.T) {
	cfg := NewConfig()
	xml := `<?xml version="1.0"?>
<config>
    <default>
        <dev>
            <js>
                <minify_files>0</minify_files>
                <minify_exclude>
                    <tiny_mce>/tiny_mce/</tiny_mce>
                    <cardinal_commerce>/v1/songbird</cardinal_commerce>
                </minify_exclude>
            </js>
        </dev>
    </default>
    <stores><default><dev><js><minify_files>1</minify_files></js></dev></default></stores>
</config>`
	if err := parseConfigXML([]byte(xml), cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Flag("dev/js/minify_files") {
		t.Error("dev/js/minify_files should be 0 (store scope must be ignored)")
	}
	excludes := cfg.List("dev/js/minify_exclude")
	if len(excludes) != 2 || excludes[0] != "/tiny_mce/" || excludes[1] != "/v1/songbird" {
		t.Errorf("unexpected excludes %v", excludes)
	}
	cfg.Set("dev/js/minify_files", "1")
	if !cfg.Flag("dev/js/minify_files") {
		t.Error("override failed")
	}
	cfg.Set("dev/css/minify_exclude", "a\n\n b ")
	if got := cfg.List("dev/css/minify_exclude"); len(got) != 2 || got[1] != "b" {
		t.Errorf("legacy newline separated list: %v", got)
	}
}
