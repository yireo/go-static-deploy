package deploy

import (
	"strings"
	"testing"
)

// A module LESS file importing a library file (like Hyva_Email::css/email.less) cannot be
// resolved by Magento either; bin/magento logs the error without failing the deployment.
func TestLessCompileErrorDoesNotFailDeployment(t *testing.T) {
	for _, strict := range []bool{false, true} {
		d, root := newFixtureDeployer(t)
		writeFixture(t, root, map[string]string{
			"modules/catalog/view/frontend/web/css/email.less": "@import 'source/lib/_lib.less';\n.e { .lib(); }\n",
		})
		d.opts.LessCompiler = LessCompilerGo
		d.opts.StrictLess = strict
		d.opts.NoTranslations, d.opts.NoJsBundle, d.opts.NoSRI = true, true, true
		p := newPackage(d, "frontend", d.env.Theme("frontend", "Vendor/child"), "en_US")

		r := d.deployPackage(p, nil, nil)
		if !strings.Contains(strings.Join(r.LessErrors, "\n"), "css/email.less") {
			t.Fatalf("strict=%v: want a LESS error for email.less, got %q (errors %q)", strict, r.LessErrors, r.Errors)
		}
		if got := len(r.Errors) > 0; got != strict {
			t.Fatalf("strict=%v: errors %q", strict, r.Errors)
		}
	}
}
