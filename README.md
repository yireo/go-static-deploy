# go-static-deploy

A fast replacement for `bin/magento setup:static-content:deploy`, written in Go. It works for **Hyvä and Luma
themes**, so it can replace the native command entirely.

It is inspired by [elgentos/magento2-static-deploy](https://github.com/elgentos/magento2-static-deploy). That tool copies files for Hyvä themes and
hands Luma themes over to `bin/magento`. This tool deploys Luma themes itself as well: it compiles LESS, builds
`requirejs-config.js`, `js-translation.json`, the JavaScript bundles and `sri-hashes.json`.

Go does the file work (Magento's file collection and fallback rules, copying, LESS preprocessing, RequireJS
config, minification, SRI hashes). The few parts that only work reliably in PHP are done by a small embedded PHP
helper script. That script never starts the full Magento application and never needs a database.

## Results

Tested on Magento 2.4.9 (Luma, Blank and Backend themes, about 2,000 files per package), production mode, `-j 4`:

| Packages                              | `bin/magento` | go-static-deploy |
|---------------------------------------|---------------|------------------|
| 3 themes × 2 locales (6 packages)     | 10.4 s        | 3.2 s            |
| 3 themes × 4 locales (12 packages)    | 15.9 s        | 3.5 s            |

Most of the remaining time is spent compiling the largest LESS file (`styles-m.less`). Compiled CSS is shared
between the locales of a theme when the LESS inputs are identical.

The output was compared file by file with the native command using `scripts/compare.sh`, in developer mode,
production mode, and production mode with minification:

- All copied files, compiled CSS (including developer mode source maps), `requirejs-config.js`,
  `requirejs-min-resolver.js`, `js-translation.json` and `sri-hashes.json` are **byte-identical** or semantically
  identical (JSON).
- The JavaScript bundles contain the same files. They are not byte-identical because Magento fills them in unsorted
  filesystem order (see [Differences](#differences-with-binmagento)).
- When JS/CSS minification is enabled, the minified file contents differ, because a different minifier is used.
- PHTML templates are never minified (like `bin/magento setup:static-content:deploy --no-html-minify`).

## Requirements

- Go 1.26+ to build
- PHP CLI that can run the Magento installation (the same PHP you use for `bin/magento`)
- `wikimedia/less.php` in the Magento vendor folder (installed with Magento by default)
- Optional: the `intl` PHP extension (used for translations that use the ICU message format, like Magento)

## Installation

```bash
make build            # creates bin/go-static-deploy
make dist             # cross-compiles linux/amd64, linux/arm64 and darwin/arm64 binaries into dist/
make test             # go vet + go test (some tests compare regular expressions with PHP, if php is available)
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
   - Imported files are trimmed.
   - The result is written to `var/view_preprocessed/pub/static/<package>/`, the same place Magento uses, so source
     maps in developer mode are identical too.
6. **RequireJS**: `requirejs-config.js` is merged in Magento's order (lib, module base/area files sorted by module
   sequence, then per theme: module overrides and the theme file). `requirejs-min-resolver.js` uses the
   `dev/js/minify_exclude` configuration.
7. **Minification** (production and default mode, when `dev/js/minify_files` or `dev/css/minify_files` is enabled) follows
   Magento's naming rules (`.min.js`, `.min.css`, exclude lists, pre-minified sources) and uses
   [tdewolff/minify](https://github.com/tdewolff/minify).
8. **SRI**: when the installed Magento_Csp module creates `sri-hashes.json`, the same file is written (sha256 of every
   deployed JavaScript file of the package, including the bundles).
9. `pub/static/deployed_version.txt` is written like Magento does.

### PHP (`internal/phpbridge/bootstrap.php`)

The script is embedded in the binary and written to `<magento-root>/var/.go-static-deploy-<pid>/` while the tool runs (so
it also works when PHP runs in a container that only has the Magento root mounted). Go starts a pool of
long-running PHP workers that read JSON lines on stdin and write JSON lines on stdout, so PHP starts only once per
worker. The workers only load the Composer autoloader:

- `config`: component registration, `config.php`/`env.php`, and optionally `core_config_data` (`dev/%`, default scope)
- `less`: compiles a materialized LESS file with `Less_Parser`, with the same options as Magento (compressed outside
  developer mode, inline source maps in developer mode). Then applies Magento's CSS `{{base_url_path}}` and
  `Module::path` URL handling with Magento's own `CssResolver`.
- `css`: the same URL handling for plain CSS files that contain `{{` or `::`
- `i18n`: builds `js-translation.json`. This uses module, language pack (with `language.xml` inheritance), theme
  and optionally database translations, and Magento's PCRE phrase patterns (read from the `di.xml` files). It
  uses PHP's `fgetcsv`, array and `json_encode` semantics and the `intl` MessageFormatter, like Magento does.
- `view-config` and `bundle`: reads the JS bundling settings with Magento's own `View` config reader and writes
  `js/bundle/bundle*.js` like `Magento\Deploy\Service\Bundle`

## Differences with `bin/magento`

- Only the `quick` strategy is implemented. `standard` and `compact` are accepted but deploy like `quick`.
- PHTML templates are never minified: the tool always behaves as if `--no-html-minify` was given. The
  `--no-html-minify` option is still accepted for compatibility, but it is ignored, and there is no way to enable
  template minification.
- **JS bundles**: Magento skips `file.js` when `file.min.js` exists, but its implementation depends on the unsorted
  filesystem order, and its cache is shared between packages. Because of this, the first deployed package of an
  area can contain a few extra non-minified files. Here the rule is applied deterministically. The result matches
  the native output of all packages except that first one.
- **Minified JS and CSS** (`dev/js/minify_files`, `dev/css/minify_files`) are created with tdewolff/minify instead of
  JShrink/CSSmin. Compiled LESS output is already compressed by `Less_Parser` in production mode.
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

`scripts/compare.sh` compares file lists and contents. It compares JSON files semantically and JavaScript bundles by
their merged contents.

## Integration tests

The GitHub Actions workflow `.github/workflows/integration.yml` builds the tool and tests it against real Magento and
Mage-OS installations. Each job installs a project with `composer create-project`, runs `bin/magento setup:install`
(with the Elasticsearch/OpenSearch modules disabled, so no search server is needed), runs `bin/go-static-deploy` in
default mode and in production mode with `--with-db`, and checks the output with `scripts/ci/check-static.sh`.

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
scripts/ci/                 install Magento and check the output in the integration tests
.github/                    integration test workflow and matrix
```
