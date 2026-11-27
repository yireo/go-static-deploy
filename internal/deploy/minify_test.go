package deploy

import (
	"strings"
	"testing"
)

func TestMinifyJSKeepsRequireName(t *testing.T) {
	mf := newMinification(true, false, nil, nil)
	cases := []string{
		`define(function (require) { var r = require('Magento_Ui/js/lib/knockout/template/renderer'); return r; });`,
		`define(['require'], function (require) { return function () { return require('mage/utils/arrays'); }; });`,
	}
	for _, src := range cases {
		out := string(mf.minifyJS([]byte(src)))
		if !strings.Contains(out, "function(require)") || !strings.Contains(out, `require("`) {
			t.Errorf("require renamed in minified output: %s", out)
		}
	}
}
