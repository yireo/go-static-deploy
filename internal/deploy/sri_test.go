package deploy

import "testing"

func TestPHPJSONObject(t *testing.T) {
	got := string(phpJSONObject(map[string]string{
		"frontend/a/é.js": "sha256-a/b+c=",
		"b\"\\":           "\n😀",
	}))
	want := `{"b\"\\":"\n\ud83d\ude00","frontend\/a\/\u00e9.js":"sha256-a\/b+c="}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestMinifiedName(t *testing.T) {
	mf := newMinification(true, false, []string{"/hugerte/"}, nil)
	cases := map[string]string{
		"frontend/A/b/en_US/js/x.js":           "frontend/A/b/en_US/js/x.min.js",
		"frontend/A/b/en_US/js/x.min.js":       "frontend/A/b/en_US/js/x.min.js",
		"frontend/A/b/en_US/hugerte/plugin.js": "frontend/A/b/en_US/hugerte/plugin.js",
		"frontend/A/b/en_US/css/styles.css":    "frontend/A/b/en_US/css/styles.css",
	}
	for in, want := range cases {
		if got := mf.addMinifiedSign(in); got != want {
			t.Errorf("addMinifiedSign(%s) = %s, want %s", in, got, want)
		}
	}
}
