# Changelog

All notable changes to this project are documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- PHTML template minification into `var/view_preprocessed/pub/static`, like `bin/magento setup:static-content:deploy`.
  Without it, Magento fails in production mode with `dev/template/minify_html` enabled (missing `root.phtml`).
- `--html-minify=auto|on|off` (`auto` by default) and `--no-html-minify` (now functional).
- `--html-minifier=php|go`: Magento's own minifier (default) or a byte-identical built-in Go port.
- GitHub releases with prebuilt binaries for linux/amd64, linux/arm64 and darwin/arm64.

### Changed
- The PHP LESS compiler (`wikimedia/less.php`) is the default.

### Fixed
- `requirejs-min-resolver.js` is included in the SRI hashes for Magento 2.4.9 and Mage-OS 3+.

[Unreleased]: https://github.com/yireo/go-static-deploy/commits/main
