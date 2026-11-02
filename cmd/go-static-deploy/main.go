// Command go-static-deploy is a fast replacement for bin/magento setup:static-content:deploy
// that supports both Hyvä and Luma based themes.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/yireo/go-static-deploy/internal/deploy"
	"github.com/yireo/go-static-deploy/internal/magento"
	"github.com/yireo/go-static-deploy/internal/phpbridge"
)

var version = "dev"

func main() {
	os.Exit(run())
}

func run() int {
	var (
		root, strategy, phpBinary, symlinkMode, mode string
		opts                                         deploy.Options
		force, withDB, lumaDispatch                  bool
		showVersion                                  bool
	)

	flag.StringVarP(&root, "root", "r", ".", "Path to the Magento root directory")
	flag.StringArrayVarP(&opts.Areas, "area", "a", nil, "Generate files only for the specified areas (repeatable, default: all)")
	flag.StringArrayVar(&opts.ExcludeAreas, "exclude-area", nil, "Do not generate files for the specified areas (repeatable)")
	flag.StringArrayVarP(&opts.Themes, "theme", "t", nil, "Generate static view files only for the specified themes (repeatable, default: all)")
	flag.StringArrayVar(&opts.ExcludeThemes, "exclude-theme", nil, "Do not generate files for the specified themes (repeatable)")
	flag.StringArrayVarP(&opts.Locales, "language", "l", nil, "Generate files only for the specified languages (repeatable)")
	flag.StringArrayVar(&opts.ExcludeLocales, "exclude-language", nil, "Do not generate files for the specified languages (repeatable)")
	flag.IntVarP(&opts.Jobs, "jobs", "j", 0, "Number of parallel jobs (0 = number of CPUs)")
	flag.StringVarP(&strategy, "strategy", "s", "quick", "Deployment strategy (only 'quick' is implemented)")
	flag.BoolVarP(&force, "force", "f", false, "Deploy files in any mode")
	flag.StringVar(&opts.ContentVersion, "content-version", "", "Custom version of static content")
	flag.BoolVar(&opts.RefreshVersion, "refresh-content-version-only", false, "Refresh the version of static content only")
	flag.BoolVar(&opts.NoJavascript, "no-javascript", false, "Do not deploy JavaScript files (also skips requirejs-config.js and js-translation.json)")
	flag.BoolVar(&opts.NoCSS, "no-css", false, "Do not deploy CSS files")
	flag.BoolVar(&opts.NoLess, "no-less", false, "Do not compile LESS files")
	flag.BoolVar(&opts.NoJsBundle, "no-js-bundle", false, "Do not create JavaScript bundles")
	// Accepted for compatibility with bin/magento only: PHTML templates are never minified.
	flag.Bool("no-html-minify", true, "Ignored: PHTML templates are never minified")
	_ = flag.CommandLine.MarkHidden("no-html-minify")
	flag.BoolVar(&opts.NoTranslations, "no-translations", false, "Do not generate js-translation.json")
	flag.BoolVar(&opts.NoSRI, "no-sri", false, "Do not generate sri-hashes.json")
	flag.BoolVarP(&opts.Verbose, "verbose", "v", false, "Verbose output")
	flag.StringVar(&phpBinary, "php", "php", "Path to the PHP binary")
	flag.BoolVar(&withDB, "with-db", false, "Also read dev/* configuration and translations from the database")
	flag.StringVar(&symlinkMode, "symlink", "", "Use symlinks instead of copies: 'file', 'locale' or 'locale-contained'")
	flag.StringVar(&mode, "mode", "", "Override the Magento mode from app/etc/env.php (developer, default, production)")
	flag.BoolVar(&lumaDispatch, "luma-dispatch", false, "Dispatch non-Hyvä themes to bin/magento setup:static-content:deploy")
	flag.BoolVar(&showVersion, "version", false, "Show the version")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [options] [languages...]\n\n", filepath.Base(os.Args[0]))
		fmt.Fprintf(os.Stderr, "Deploys static view files of Hyvä and Luma themes (compatible with bin/magento setup:static-content:deploy)\n\nOptions:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if showVersion {
		fmt.Println(version)
		return 0
	}

	switch symlinkMode {
	case deploy.SymlinkNone, deploy.SymlinkFile, deploy.SymlinkLocale, deploy.SymlinkLocaleContained:
	default:
		fmt.Fprintf(os.Stderr, "Error: --symlink must be 'file', 'locale' or 'locale-contained', got '%s'\n", symlinkMode)
		return 1
	}
	if strategy != "quick" {
		fmt.Fprintf(os.Stderr, "Warning: strategy %q is not implemented, using 'quick'\n", strategy)
	}
	opts.SymlinkMode = symlinkMode
	opts.Mode = mode
	opts.Locales = append(opts.Locales, flag.Args()...)
	if opts.Jobs <= 0 {
		opts.Jobs = runtime.NumCPU()
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	if _, err := os.Stat(filepath.Join(absRoot, "app", "etc", "config.php")); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s does not look like a Magento root (app/etc/config.php not found)\n", absRoot)
		return 1
	}

	start := time.Now()
	bridge, err := phpbridge.New(phpBinary, absRoot, withDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	defer bridge.Cleanup()

	env, err := magento.Load(bridge)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	effectiveMode := env.Mode
	if mode != "" {
		effectiveMode = mode
	}
	if effectiveMode != "production" && !force {
		fmt.Fprintf(os.Stderr, "Error: deploying static content is applicable only for production mode (current mode: %s). Use -f to force.\n", effectiveMode)
		return 1
	}
	if !env.HasLessPHP && !opts.NoLess && !opts.NoCSS {
		fmt.Fprintln(os.Stderr, "Warning: wikimedia/less.php is not installed; LESS files cannot be compiled")
	}

	pool := bridge.NewPool(opts.Jobs)
	defer pool.Close()

	out := os.Stdout
	d := deploy.New(env, pool, withDB, opts, out)

	if opts.Verbose {
		fmt.Fprintf(out, "go-static-deploy %s\n", version)
		fmt.Fprintf(out, "Root: %s\nMode: %s\nPHP: %s (%s)\nJobs: %d\n", absRoot, effectiveMode, phpBinary, env.PHPVersion, opts.Jobs)
		if symlinkMode != "" {
			fmt.Fprintf(out, "Symlink mode: %s\n", symlinkMode)
		}
		fmt.Fprintln(out)
	}

	contentVersion, err := d.WriteVersion()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error writing deployed_version.txt: %v\n", err)
		return 1
	}
	if opts.RefreshVersion {
		fmt.Fprintf(out, "New content version: %s\n", contentVersion)
		return 0
	}
	opts.ContentVersion = contentVersion

	groups, err := d.Packages()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	var goGroups [][]*deploy.Package
	var dispatch []*deploy.Package
	for _, group := range groups {
		theme := group[0].Theme
		hyva := theme.IsHyva()
		if opts.Verbose {
			kind := "Luma"
			if hyva {
				kind = "Hyvä"
			}
			fmt.Fprintf(out, "🎨 %s detected as %s theme\n", theme.FullPath(), kind)
		}
		if !hyva && lumaDispatch {
			dispatch = append(dispatch, group...)
			continue
		}
		goGroups = append(goGroups, group)
	}
	if opts.Verbose {
		fmt.Fprintln(out)
	}

	exitCode := 0
	var results []deploy.Result
	if len(goGroups) > 0 {
		results = d.Run(goGroups)
		if printSummary(out, results, time.Since(start), opts.Verbose) {
			exitCode = 1
		}
	}

	if len(dispatch) > 0 {
		if err := dispatchToMagento(absRoot, phpBinary, dispatch, opts, contentVersion, out); err != nil {
			fmt.Fprintf(os.Stderr, "Error deploying themes with bin/magento: %v\n", err)
			exitCode = 1
		}
	}

	return exitCode
}

func printSummary(out *os.File, results []deploy.Result, total time.Duration, verbose bool) bool {
	hasErrors := false
	var files int64
	fmt.Fprintln(out)
	for _, r := range results {
		files += r.Files
		if len(r.Errors) > 0 {
			hasErrors = true
			fmt.Fprintf(out, "✗ %s\n", r.Package)
			for _, e := range r.Errors {
				fmt.Fprintf(out, "    %s\n", e)
			}
		}
		if verbose {
			for _, w := range r.Warnings {
				fmt.Fprintf(out, "  ⚠ %s: %s\n", r.Package, w)
			}
		}
	}
	fmt.Fprintf(out, "Deployed %d packages, %d files in %.2fs\n", len(results), files, total.Seconds())
	return hasErrors
}

func dispatchToMagento(root, php string, packages []*deploy.Package, opts deploy.Options, contentVersion string, out *os.File) error {
	args := []string{"bin/magento", "setup:static-content:deploy", "-f", "--content-version=" + contentVersion, "--no-html-minify"}
	var areas, themes, locales []string
	for _, p := range packages {
		if !slices.Contains(areas, p.Area) {
			areas = append(areas, p.Area)
		}
		if !slices.Contains(themes, p.Theme.Code) {
			themes = append(themes, p.Theme.Code)
		}
		if !slices.Contains(locales, p.Locale) {
			locales = append(locales, p.Locale)
		}
	}
	for _, a := range areas {
		args = append(args, "--area="+a)
	}
	for _, t := range themes {
		args = append(args, "--theme="+t)
	}
	for flagName, enabled := range map[string]bool{
		"--no-javascript": opts.NoJavascript,
		"--no-css":        opts.NoCSS,
		"--no-less":       opts.NoLess,
		"--no-js-bundle":  opts.NoJsBundle,
	} {
		if enabled {
			args = append(args, flagName)
		}
	}
	args = append(args, fmt.Sprintf("--jobs=%d", opts.Jobs))
	args = append(args, locales...)
	fmt.Fprintf(out, "Executing: %s %s\n", php, strings.Join(args, " "))
	cmd := exec.Command(php, args...)
	cmd.Dir = root
	cmd.Stdout = out
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
