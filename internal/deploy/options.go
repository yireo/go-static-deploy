package deploy

// Symlink modes
const (
	SymlinkNone            = ""
	SymlinkFile            = "file"
	SymlinkLocale          = "locale"
	SymlinkLocaleContained = "locale-contained"
)

// Options configures a deployment. The names follow bin/magento setup:static-content:deploy.
type Options struct {
	Areas          []string
	ExcludeAreas   []string
	Themes         []string
	ExcludeThemes  []string
	Locales        []string
	ExcludeLocales []string
	Jobs           int
	ContentVersion string
	RefreshVersion bool
	NoJavascript   bool
	NoCSS          bool
	NoLess         bool
	NoJsBundle     bool
	NoTranslations bool
	NoSRI          bool
	NoClean        bool
	SymlinkMode    string
	Verbose        bool
	// Mode overrides the Magento mode found in app/etc/env.php (developer, production, default).
	Mode string
}
