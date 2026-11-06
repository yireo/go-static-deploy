# go-static-deploy

A fast replacement for `bin/magento setup:static-content:deploy`, written in Go. It works for **Hyvä and Luma
themes**, so it can replace the native command entirely.

It is inspired by [elgentos/magento2-static-deploy](https://github.com/elgentos/magento2-static-deploy). That tool copies files for Hyvä themes and
hands Luma themes over to `bin/magento`. This tool deploys Luma themes itself as well: it compiles LESS, builds
`requirejs-config.js`, `js-translation.json`, the JavaScript bundles and `sri-hashes.json`.

Go does the file work (Magento's file collection and fallback rules, copying, LESS preprocessing and compilation,
CSS URL handling, RequireJS config, minification, SRI hashes). The few parts that only work reliably in PHP are done by a small embedded PHP
helper script. That script never starts the full Magento application and never needs a database. See
[Where PHP is still used](#where-php-is-still-used) for the exact list.

## Results

Tested on Magento 2.4.9 (Luma, Blank and Backend themes, about 2,000 files per package), production mode, `-j 4`:

| Packages                              | `bin/magento` | go-static-deploy |
|---------------------------------------|---------------|------------------|
| 3 themes × 2 locales (6 packages)     | 10.4 s        | 3.2 s            |
| 3 themes × 4 locales (12 packages)    | 15.9 s        | 3.5 s            |

These results were measured with the default LESS compiler (less.php). Most of the remaining time is spent
compiling the largest LESS file (`styles-m.less`). Compiled CSS is shared between the locales of a theme when the
LESS inputs are identical. The experimental Go LESS compiler (`--less-compiler=go`) is faster, but its output still
differs (see [LESS compilers](#less-compilers)).

The output was compared file by file with the native command using `scripts/compare.sh`, in developer mode,
production mode, and production mode with minification:

- All copied files, compiled CSS (with less.php, including developer mode source maps), `requirejs-config.js`,
  `requirejs-min-resolver.js`, `js-translation.json` and `sri-hashes.json` are **byte-identical** or semantically
  identical (JSON).
- The JavaScript bundles contain the same files. They are not byte-identical because Magento fills them in unsorted
  filesystem order (see [Differences](#differences-with-binmagento)).
- When JS/CSS minification is enabled, the minified file contents differ, because a different minifier is used.
- PHTML templates are never minified (like `bin/magento setup:static-content:deploy --no-html-minify`).

## Requirements

- Go 1.26+ to build
- PHP CLI that can run the Magento installation (the same PHP you use for `bin/magento`). See
  [Where PHP is still used](#where-php-is-still-used).
- `wikimedia/less.php` in the Magento vendor folder (installed with Magento by default), to compile LESS like
  Magento. Without it, the experimental Go LESS compiler is used (see [LESS compilers](#less-compilers)).
- Optional: the `intl` PHP extension (used for translations that use the ICU message format, like Magento)

## Installation

```bash
make build            # creates bin/go-static-deploy
make dist             # cross-compiles linux/amd64, linux/arm64 and darwin/arm64 binaries into dist/
make test             # go vet + go test (some tests compare regular expressions with PHP, if php is available)
```

The tests in `internal/deploy/less_test.go` and `internal/deploy/cssurls_test.go` run `php -r` to compare the Go
regular expressions with Magento's PCRE expressions. They are skipped when `php` is not in the `PATH`.

`TestCompileLessGoMatchesLessPHP` compares less.go with less.php for the LESS files that a previous deployment left
in `var/view_preprocessed`. It only runs when `GSD_TEST_MAGENTO_ROOT` points to a Magento root:

```bash
GSD_TEST_MAGENTO_ROOT=/var/www/magento go test ./internal/deploy -run TestCompileLessGoMatchesLessPHP -v
```

The binaries are built with `CGO_ENABLED=0`, so they are statically linked and also run on musl-based systems like Alpine.

## Usage

The command line follows `bin/magento setup:static-content:deploy`:

```bash
# All themes of all areas, two locales
./go-static-deploy -r /var/www/magento nl_NL en_US

# Specific areas and themes, 8 jobs, verbose output
./go-static-deploy -f -a frontend -a adminhtml -t Magento/luma -t Magento/backend -j 8 -v nl_NL en_US

# Hyvä theme: JavaScript bundles are not used by Hyvä, so skip them
./go-static-deploy -a frontend -t Vendor/hyva --no-js-bundle nl_NL en_US
```

Main options:

| Option | Description |
|--------|-------------|
| `-r, --root` | Magento root directory (default: current directory) |
| `-a, --area`, `--exclude-area` | Areas to deploy (default: all areas with themes) |
| `-t, --theme`, `--exclude-theme` | Themes to deploy (default: all themes) |
| `-l, --language`, `--exclude-language`, positional arguments | Locales to deploy (default: `en_US`) |
| `-j, --jobs` | Number of packages deployed in parallel and number of PHP workers (default: number of CPUs) |
| `-f, --force` | Required when Magento is not in production mode, like `bin/magento` |
| `--mode` | Override `MAGE_MODE` from `app/etc/env.php` |
| `--less-compiler` | `auto` (default: `php` when less.php is installed, otherwise `go`), `php` (`wikimedia/less.php`, like Magento) or `go` (experimental, built-in [less.go](https://github.com/toakleaf/less.go)). See [LESS compilers](#less-compilers) |
| `--content-version`, `--refresh-content-version-only` | Same as `bin/magento` |
| `--no-javascript`, `--no-css`, `--no-less`, `--no-js-bundle` | Skip parts of the deployment |
| `--no-translations`, `--no-sri` | Skip `js-translation.json` or `sri-hashes.json` |
| `--with-db` | Also read `dev/*` configuration and the `translation` table from the database (PDO, no Magento bootstrap) |
| `--symlink` | `file`, `locale` or `locale-contained` (see below) |
| `--luma-dispatch` | Hand non-Hyvä themes over to `bin/magento setup:static-content:deploy`, like the elgentos tool does |
| `--php` | PHP binary (default: `php`) |
| `-v, --verbose` | Per-package progress, theme type (Hyvä/Luma) and warnings |

### Symlink modes

- `file`: copied files become relative symlinks to their source files. Generated files (compiled CSS, minified
  files, RequireJS config, translations, bundles) are always real files.
- `locale`: the first locale of a theme is deployed with per-file symlinks. Other locales become a directory symlink
  to the first locale when their output is the same: no locale-specific files (`i18n/<locale>` folders), the same
  `js-translation.json`, no developer mode source maps, and no `sri-hashes.json` (that file contains the locale in its
  paths, so Magento_Csp must be disabled or `--no-sri` must be used). When the output differs, the locale is
  deployed in full.
- `locale-contained`: same as `locale`, but with real files instead of per-file symlinks, so `pub/static` does not
  depend on the source tree.

Your web server must follow symlinks in `pub/static` (Nginx does by default; Apache needs `Options +FollowSymlinks`).

## How it works

### Go

1. **Installation details**: the PHP helper prints the registered components (modules, themes, language packs and
   libraries from `registration.php` files), the module list of `app/etc/config.php`, the mode and the
   `system` configuration from `config.php` and `env.php`. Go reads `theme.xml` (parent themes) and the `<default>`
   section of the `etc/config.xml` files of enabled modules.
2. **Packages**: one package per area, theme and locale.
3. **File collection** mirrors `Magento\Deploy\Collector\Collector` with package inheritance. A package gets the files of
   `lib/web` (except `css/docs`), `view/base/web` and `view/<area>/web` of enabled modules, the locale-specific
   module files, and the `web` and `<Vendor_Module>/web` folders of the theme and its parents. `.less` files
   are deployed as `.css`; LESS partials (`_*.less`) are skipped. Hidden files are skipped, like Magento's glob.
4. **Fallback resolution** mirrors Magento's static view file fallback rule: theme chain (`i18n/<locale>` first), then
   `lib/web`. For module files: theme chain `<Vendor_Module>/web`, then the module's `view/<area>/web` and
   `view/base/web` (with `i18n/<locale>`). Directory listings are cached and shared between packages.
5. **LESS preprocessing** mirrors Magento's `magento_import` and `import` instructions:
   - `@magento_import` collects library, module (sorted by module sequence) and theme override files, including
     Magento's file list collation quirks.
   - Imports are resolved and `Module::path` notation is converted to relative paths.
   - Comments are removed with the same semantics as Magento's regular expression.
   - Imported files are trimmed, like Magento 2.4.9 and later (`View\Asset\File::getContent()`). Older versions
     keep them as they are. The PHP helper detects this in the installed framework.
   - The result is written to `var/view_preprocessed/pub/static/<package>/`, the same place Magento uses, so source
     maps in developer mode are identical too.
   - The materialized root files are compiled with less.php (default) or less.go (`--less-compiler=go`). See
     [LESS compilers](#less-compilers).
   - **CSS URL handling** mirrors Magento's `CssResolver` with the `{{base_url_path}}` and `Module::path` notations,
     for compiled LESS and for plain CSS files that contain `{{` or `::`.
6. **RequireJS**: `requirejs-config.js` is merged in Magento's order (lib, module base/area files sorted by module
   sequence, then per theme: module overrides and the theme file). `requirejs-min-resolver.js` uses the
   `dev/js/minify_exclude` configuration.
7. **Minification** (production and default mode, when `dev/js/minify_files` or `dev/css/minify_files` is enabled) follows
   Magento's naming rules (`.min.js`, `.min.css`, exclude lists, pre-minified sources) and uses
   [tdewolff/minify](https://github.com/tdewolff/minify). Excludes added by plugins on
   `Magento\Framework\View\Asset\Minification::getExcludes()` in global `etc/di.xml` files (like the
   `/pay.google.com/` exclude of PayPal_BraintreeCore) are included as well: they also end up in
   `requirejs-min-resolver.js`.
8. **SRI**: when the installed Magento_Csp module creates `sri-hashes.json`, the same file is written. Magento
   2.4.6 - 2.4.8 store the hashes per area in `pub/static/<area>/sri-hashes.json`: the files of all areas are removed when
   the deployment starts, and every package adds the sha256 hashes of its JavaScript files (source files under their
   name without `.min`, generated files and bundles as deployed, except `requirejs-min-resolver.js`). Magento 2.4.9
   and Mage-OS 3 and later write a `sri-hashes.json` in every package with the hashes of the deployed JavaScript
   files (under their deployed name, except `requirejs-min-resolver.js`). Older versions write a `sri-hashes.json`
   in every package with the hashes of all deployed JavaScript files.
9. `pub/static/deployed_version.txt` is written like Magento does.

### PHP (`internal/phpbridge/bootstrap.php`)

The script is embedded in the binary and written to `<magento-root>/var/.go-static-deploy-<pid>/` while the tool runs (so
it also works when PHP runs in a container that only has the Magento root mounted). Go starts a pool of
long-running PHP workers that read JSON lines on stdin and write JSON lines on stdout, so PHP starts only once per
worker. The number of workers equals `-j`. The script only loads the Composer autoloader. It never bootstraps the
Magento application.

### Where PHP is still used

Everything not listed here is done in Go: file collection and fallback, LESS preprocessing (`@magento_import`,
imports, comment removal), LESS compilation (with `--less-compiler=go`), CSS URL handling, `requirejs-config.js`,
`requirejs-min-resolver.js`, minification, `sri-hashes.json` and `deployed_version.txt`.

| PHP command | When it is called | What PHP does | Skipped with |
|-------------|-------------------|---------------|--------------|
| `config` | Once at startup, as a separate process (`internal/magento/env.go`) | Reads component registration (`registration.php` files), `app/etc/config.php`/`env.php` and the PHP version. Calls the minification exclude plugins of enabled modules (only plugins without constructor dependencies, called with an empty list) and checks whether the framework trims asset contents and whether its LESS processor generates source maps. With `--with-db`, also reads `core_config_data` (`dev/%`, default scope). | Always called |
| `less` | Once per LESS root file of a package (`compileLess` in `internal/deploy/deployer.go`), when less.php is installed. Outside developer mode, the result is shared between the locales of a theme when the inputs are identical. | Compiles the materialized LESS file with `Less_Parser`, with the same options as Magento (compressed outside developer mode, inline source maps in developer mode on Magento 2.4.8 and later). | `--no-less`, `--no-css`, `--less-compiler=go` (except for developer mode source maps) |
| `i18n` | Once per package (`jsTranslation` in `internal/deploy/i18n.go`). With `--symlink locale` or `locale-contained`, also used to check whether a locale can be a symlink to the first locale. | Builds `js-translation.json` from module, language pack (with `language.xml` inheritance), theme and optionally database translations (`translation` table, with `--with-db`), using Magento's PCRE phrase patterns from the `di.xml` files. It uses PHP's `fgetcsv`, array and `json_encode` semantics and the `intl` MessageFormatter, like Magento does. | `--no-translations`, `--no-javascript` |
| `view-config` | Once per theme, cached (`viewConfig` in `internal/deploy/bundle.go`) | Merges the JS bundling settings of the `etc/view.xml` files with Magento's own `View` config reader | `--no-js-bundle`, `--no-javascript` |
| `bundle` | Once per package (`bundle` in `internal/deploy/bundle.go`) | Writes `js/bundle/bundle*.js` like `Magento\Deploy\Service\Bundle` | `--no-js-bundle`, `--no-javascript` |

The database is only accessed with `--with-db`, and only with PDO (`config` and `i18n`).

PHP is also used outside the helper script in one case: with `--luma-dispatch`, non-Hyvä themes are deployed by
running `php bin/magento setup:static-content:deploy` (`dispatchToMagento` in `cmd/go-static-deploy/main.go`).
This is the only case where the full Magento application is bootstrapped.

To keep PHP usage to a minimum (for example for Hyvä themes), use `--no-js-bundle` and, if you don't need
`js-translation.json`, `--no-translations`. PHP is then only used for the `config` call at startup.

## LESS compilers

`--less-compiler` selects how the materialized LESS files are compiled. The LESS preprocessing (`@magento_import`,
imports) and the CSS URL handling are the same for both.

- `auto` (default): `php` when less.php is installed, otherwise `go` (with a warning).
- `php`: `wikimedia/less.php` through the PHP helper, the same library and options as Magento, so the CSS is
  identical to `bin/magento`. Like Magento 2.4.8 and later, inline source maps are generated in developer mode;
  older Magento versions do not generate them, and neither does this tool for those versions.
- `go` (experimental): [less.go](https://github.com/toakleaf/less.go), a Go port of less.js 4, built into the
  binary. It is compiled with the same options as Magento: math is always evaluated (also divisions outside
  parentheses, like less.php), URLs are not rewritten, and the output is compressed outside developer mode. LESS
  files are compiled in parallel without PHP. In one test, a Luma package deployed in 10 s instead of 36 s with
  less.php (with `styles-l.less` failing, see below).

When `php` is selected but less.php is not installed, `go` is used with a warning. When `go` is selected in
developer mode and Magento generates source maps, less.php is still used when it is installed, because less.go
cannot create source maps in combination with Magento's math setting. Without less.php, developer mode CSS is
compiled with less.go, without source maps.

**less.go is young, and its output differs from less.php.** Tested with Magento 2.4 Luma and Blank:

- `styles-l.less` fails to compile (`Error evaluating function isunit`), because of a less.go scoping bug with a
  variable that is set inside a mixin in `lib/web/css/source/lib/_layout.less`. The error is reported and the exit
  code is 1, but `styles-l.css` is missing.
- `email.less` compiles, but whole blocks of rules are missing, including the `@import` of `email-fonts.css`.
- `styles-m.less` compiles with differences in number formatting (for example `0` instead of `0rem` and less
  floating point noise) and in some rules.
- The backend theme compiles without errors.

Don't use `--less-compiler=go` for Luma based themes until these issues are fixed in less.go.

## Differences with `bin/magento`

- Only the `quick` strategy is implemented. `standard` and `compact` are accepted but deploy like `quick`.
- PHTML templates are never minified: the tool always behaves as if `--no-html-minify` was given. The
  `--no-html-minify` option is still accepted for compatibility, but it is ignored, and there is no way to enable
  template minification.
- **JS bundles**: Magento skips `file.js` when `file.min.js` exists, but its implementation depends on the unsorted
  filesystem order, and its cache is shared between packages. Because of this, the first deployed package of an
  area can contain a few extra non-minified files. Here the rule is applied deterministically. The result matches
  the native output of all packages except that first one.
- **Compiled LESS** is identical to the output of `bin/magento` with less.php (the default). With
  `--less-compiler=go`, or when less.php is not installed, it differs. See [LESS compilers](#less-compilers).
- **Minified JS and CSS** (`dev/js/minify_files`, `dev/css/minify_files`) are created with tdewolff/minify instead of
  JShrink/CSSmin. Compiled LESS output is already compressed by the LESS compiler in production mode.
- Configuration comes from `etc/config.xml` defaults and `app/etc/config.php`/`env.php`. Database values are only
  read with `--with-db` (only the `dev/*` paths in the default scope). Run `bin/magento app:config:dump` to keep the
  database out of the build.
- With `--with-db`, translations from the `translation` table are read for store 0, plus the default store view for
  the frontend.
- The `<theme_pubstatic_dir>` fallback (an existing CSS file in `pub/static` from a previous deployment) is not
  used: package directories are always cleaned before deploying.
- Errors in a single file are reported, and the other files are still deployed. The exit code is 1 when any error
  occurred.

## Comparing the output with `bin/magento`

```bash
php bin/magento setup:static-content:deploy -a frontend -t Magento/luma en_US nl_NL
cp -a pub/static /tmp/native-static
go-static-deploy -a frontend -t Magento/luma en_US nl_NL
scripts/compare.sh /tmp/native-static pub/static
```

`scripts/compare.sh` compares file lists and contents of all packages, and `pub/static/<area>/sri-hashes.json`. It
compares JSON files semantically and JavaScript bundles by their merged contents. Bundle entries `file.js` for which
`file.min.js` exists are ignored (see [Differences](#differences-with-binmagento)). With `--minified` (JS/CSS
minification enabled), the contents of `.min.js`/`.min.css` files, their SRI hashes and the bundled contents are not
compared, only the file names.

## Integration tests

The GitHub Actions workflow `.github/workflows/integration.yml` builds the tool and tests it against real Magento and
Mage-OS installations. Each job installs a project with `composer create-project` and runs `bin/magento setup:install`
(with the Elasticsearch/OpenSearch modules disabled, so no search server is needed). The fixture theme `Test/child`
(`scripts/ci/fixtures/`, extends Luma) is installed as well. It has theme and module level LESS (`_extend.less`,
a `Module::` URL, a LESS variable), images, a JavaScript file with phrases translated in `i18n/nl_NL.csv` and in the
database, an override of a `Magento_Ui` template and a `requirejs-config.js`.

Then `scripts/ci/test-mode.sh` runs one step per mode:

| Mode | bin/magento | go-static-deploy |
|------|-------------|------------------|
| `default` | default mode | default mode |
| `developer` | developer mode | developer mode |
| `production` | production mode | `--with-db` |
| `production-minify` | production mode, `dev/js/minify_files` and `dev/css/minify_files` in the database | `--with-db` |
| `symlink` | - | `--symlink file`, `--symlink locale --no-sri`, `--symlink locale-contained --no-sri` |

In each mode, the output of `bin/magento setup:static-content:deploy` is compared with the output of go-static-deploy
using `scripts/compare.sh`. The symlink modes are compared with a regular go-static-deploy deployment. The output is
also validated on its own by `scripts/ci/validate-static.php`:

- required files (compiled CSS, `requirejs-config.js`, `js-translation.json`, library files, minified names) and
  `deployed_version.txt`; no leftover temporary directories of the PHP helper
- `sri-hashes.json` (per package or per area, like the installed Magento_Csp module): every deployed JavaScript file
  is listed, and every hash is recalculated
- JavaScript bundles: valid pools, every entry is a deployed file with the same content, no duplicates, no `file.js`
  when `file.min.js` exists, the RequireJS bundle configuration in the last bundle only
- CSS: no unresolved `@magento_import`, `Module::` URLs or `.less` imports; every relative `url()` of the compiled
  `css/*.css` files exists (problems that also exist in the `bin/magento` output are ignored)
- source maps in developer mode only (and only on Magento versions that generate them); `.min.js`/`.min.css` names with minification
- symlink modes: symlinked files resolve, generated files are real files, locale directory symlinks point to a
  deployed sibling locale
- the fixture theme: compiled LESS, `Module::` URL resolution, template override, RequireJS config, translations from
  the theme CSV and (with `--with-db`) from the database, and no fixture content in the parent themes

`bin/magento` runs with one job (`NATIVE_JOBS`): with more jobs, its forked processes overwrite each other's
`<area>/sri-hashes.json`.

The matrix is configured in `.github/test-matrix.json`. Each entry is one job:

```json
{
  "name": "Magento 2.4.8-p5 / PHP 8.4 / MariaDB 11.4",
  "repository_url": "https://mirror.mage-os.org/",
  "project_package": "magento/project-community-edition",
  "version": "2.4.8-p5",
  "php_image": "php:8.4-cli",
  "db_image": "mariadb:11.4"
}
```

- `repository_url`: Composer repository. Magento uses the public `https://mirror.mage-os.org/` mirror, Mage-OS uses
  `https://repo.mage-os.org/`. To use `https://repo.magento.com/`, add a `COMPOSER_AUTH` repository secret with the
  authentication JSON.
- `php_image`: an official `php:*-cli` image (extensions are installed with `docker-php-extension-installer`)
- `db_image`: a `mysql:*` or `mariadb:*` image

The scripts in `scripts/ci/` can also be run locally inside a PHP container.

## Project structure

```
cmd/go-static-deploy/       command line interface
internal/magento/           installation details: components, modules, themes, configuration
internal/deploy/            packages, file collection and fallback, LESS preprocessing, RequireJS,
                            translations, bundles, minification, SRI
internal/phpbridge/         embedded PHP helper script and worker pool
scripts/compare.sh          compare two pub/static trees
scripts/ci/                 integration tests: install Magento, test a mode, validate the output, fixture theme
.github/                    integration test workflow and matrix
```
