<?php
/**
 * Validate the pub/static output of go-static-deploy on its own, without comparing it with bin/magento.
 *
 * Usage: php scripts/ci/validate-static.php [options] <magento-root> <locale>...
 *   --mode=<developer|default|production>      Magento mode of the deployment (default: default)
 *   --minify                                   JS and CSS minification were enabled
 *   --no-sri                                   The deployment ran with --no-sri
 *   --symlink=<file|locale|locale-contained>   The deployment ran with --symlink
 *   --reference=<pub/static>                   Output of bin/magento: CSS problems that also exist there are ignored
 *   --db-translation                           The fixture phrase from the translation table must be translated
 */

declare(strict_types=1);

final class StaticValidator
{
    private const FIXTURE_THEME = 'frontend/Test/child';
    private const FIXTURE_PHRASE = ['GSD fixture phrase', 'GSD vertaalde zin'];
    private const DB_PHRASE = ['GSD database phrase', 'GSD database vertaling'];

    private string $static;
    private array $errors = [];
    private bool $sri;
    /** 'area': pub/static/<area>/sri-hashes.json (Magento_Csp with SubresourceIntegrity storage), 'package': per package */
    private string $sriLayout;
    /** @var array<string, array<string, string>> expected SRI hashes per area */
    private array $sriExpected = [];
    /** @var array<string, array<string, bool>> optional SRI entries per area (written after Magento's post processors) */
    private array $sriOptional = [];

    public function __construct(
        private string $root,
        private array $locales,
        private array $options
    ) {
        $this->static = $root . '/pub/static';
        $this->sri = !$options['no-sri'] && $this->sriInstalled();
        $this->sriLayout = glob($root . '/vendor/*/module-csp/Model/SubresourceIntegrity/Storage/File.php') ? 'area' : 'package';
    }

    public function run(): int
    {
        $version = @file_get_contents($this->static . '/deployed_version.txt');
        if ($version === false || !preg_match('/^\d+$/', $version)) {
            $this->fail('pub/static/deployed_version.txt is missing or not numeric');
        }
        foreach (glob($this->root . '/var/.go-static-deploy-*') ?: [] as $dir) {
            $this->fail("temporary directory $dir was not removed");
        }

        $packages = $this->packages();
        foreach ($this->locales as $locale) {
            foreach (['frontend/Magento/blank', 'frontend/Magento/luma', self::FIXTURE_THEME, 'adminhtml/Magento/backend'] as $theme) {
                if (!in_array("$theme/$locale", $packages, true)) {
                    $this->fail("package $theme/$locale was not deployed");
                }
            }
        }
        foreach ($packages as $package) {
            $this->validatePackage($package);
        }
        foreach (['frontend', 'adminhtml'] as $area) {
            $file = "{$this->static}/$area/sri-hashes.json";
            if ($this->sri && $this->sriLayout === 'area') {
                echo "== $area/sri-hashes.json\n";
                $this->validateSriFile($file, $this->sriExpected[$area] ?? [], $this->sriOptional[$area] ?? []);
            } elseif (file_exists($file)) {
                $this->fail("$area/sri-hashes.json exists, but " . ($this->sri ? 'this Magento version writes it per package' : 'SRI is disabled'));
            }
        }

        if ($this->errors) {
            fwrite(STDERR, count($this->errors) . " check(s) failed\n");
            return 1;
        }
        echo 'All checks passed for ' . count($packages) . ' packages (' . implode(', ', $this->modeDescription()) . ")\n";
        return 0;
    }

    private function modeDescription(): array
    {
        $result = ['mode ' . $this->options['mode']];
        foreach (['minify', 'db-translation'] as $flag) {
            if ($this->options[$flag]) {
                $result[] = $flag;
            }
        }
        if ($this->options['symlink'] !== '') {
            $result[] = 'symlink ' . $this->options['symlink'];
        }
        $result[] = $this->sri ? "SRI per {$this->sriLayout}" : 'no SRI';
        return $result;
    }

    private function fail(string $message): void
    {
        $this->errors[] = $message;
        fwrite(STDERR, "FAIL: $message\n");
    }

    private function sriInstalled(): bool
    {
        if (!glob($this->root . '/vendor/*/module-csp/Model/Deploy/Package/Processor/PostProcessor/Integrity.php')) {
            return false;
        }
        $config = require $this->root . '/app/etc/config.php';
        return !empty($config['modules']['Magento_Csp']);
    }

    /** @return string[] package paths like frontend/Magento/luma/en_US */
    private function packages(): array
    {
        $packages = [];
        foreach (['frontend', 'adminhtml'] as $area) {
            foreach (glob($this->static . "/$area/*/*", GLOB_ONLYDIR) ?: [] as $themeDir) {
                foreach ($this->locales as $locale) {
                    if (is_dir("$themeDir/$locale")) {
                        $packages[] = substr("$themeDir/$locale", strlen($this->static) + 1);
                    }
                }
            }
        }
        sort($packages);
        return $packages;
    }

    /** @return string[] relative paths of all files in a directory (following symlinks) */
    private function listFiles(string $dir): array
    {
        $files = [];
        $iterator = new RecursiveIteratorIterator(new RecursiveDirectoryIterator(
            $dir,
            FilesystemIterator::SKIP_DOTS | FilesystemIterator::FOLLOW_SYMLINKS
        ));
        foreach ($iterator as $file) {
            if ($file->isFile()) {
                $files[] = substr($file->getPathname(), strlen($dir) + 1);
            }
        }
        sort($files);
        return $files;
    }

    /** Deployed name of a generated file: Magento adds .min to generated JS/CSS files when minification is on. */
    private function generated(string $rel): string
    {
        if ($this->options['minify'] && preg_match('/\.(js|css)$/', $rel, $m)) {
            return substr($rel, 0, -strlen($m[1])) . 'min.' . $m[1];
        }
        return $rel;
    }

    private function validatePackage(string $package): void
    {
        $dir = $this->static . '/' . $package;
        $files = $this->listFiles($dir);
        $set = array_flip($files);
        [$area, , , $locale] = explode('/', $package);
        $theme = dirname($package);
        echo "== $package: " . count($files) . " files\n";

        if (count($files) < 500) {
            $this->fail("$package has only " . count($files) . ' files');
        }

        $this->validateRequiredFiles($package, $area, $theme, $set);
        $this->validateTranslations($package, $theme, $locale, $dir);
        $this->validateCss($package, $dir, $files);
        $this->validateBundles($package, $dir, $files, $set);
        $this->validateSri($package, $dir, $files);
        $this->validateSymlinks($package, $theme, $locale, $dir, $files);
        if ($theme === self::FIXTURE_THEME) {
            $this->validateFixture($package, $dir, $set);
        } elseif ($area === 'frontend') {
            $this->validateNoFixture($package, $dir);
        }
    }

    private function mainCss(string $area, string $theme): array
    {
        if ($area === 'frontend') {
            return ['css/styles-m.css', 'css/styles-l.css', 'css/print.css', 'css/email.css', 'css/email-inline.css'];
        }
        return $theme === 'adminhtml/Magento/backend' ? ['css/styles.css'] : [];
    }

    private function validateRequiredFiles(string $package, string $area, string $theme, array $set): void
    {
        $required = ['js-translation.json', $this->generated('requirejs-config.js')];
        foreach ($this->mainCss($area, $theme) as $css) {
            $required[] = $this->generated($css);
        }
        if ($this->options['minify']) {
            $required[] = 'requirejs-min-resolver.min.js';
            foreach (['requirejs-config.js', 'requirejs-min-resolver.js'] as $file) {
                if (isset($set[$file])) {
                    $this->fail("$package/$file exists, while JS minification is enabled");
                }
            }
        }
        foreach ($required as $file) {
            if (!isset($set[$file]) || filesize($this->static . "/$package/$file") === 0) {
                $this->fail("$package/$file is missing or empty");
            }
        }
        // Library files: pre-minified sources or minified copies are fine
        foreach (['requirejs/require.js', 'jquery.js', 'mage/requirejs/mixins.js', 'mage/translate.js'] as $file) {
            $min = substr($file, 0, -3) . '.min.js';
            if (!isset($set[$file]) && !isset($set[$min])) {
                $this->fail("$package/$file is missing");
            }
            if ($this->options['minify'] && !isset($set[$min]) && $file !== 'requirejs/require.js') {
                $this->fail("$package/$min is missing, while JS minification is enabled");
            }
        }

        $minified = array_filter(array_keys($set), fn($f) => str_ends_with((string)$f, '.min.js'));
        if ($this->options['minify'] && count($minified) < 100) {
            $this->fail("$package has only " . count($minified) . ' .min.js files, while JS minification is enabled');
        }

        $styles = $this->static . "/$package/" . $this->generated($area === 'frontend' ? 'css/styles-m.css' : 'css/styles.css');
        if (is_file($styles)) {
            $hasMap = str_contains((string)file_get_contents($styles), 'sourceMappingURL=');
            if ($this->options['mode'] === 'developer' && !$hasMap) {
                $this->fail("$styles has no source map in developer mode");
            } elseif ($this->options['mode'] !== 'developer' && $hasMap) {
                $this->fail("$styles has a source map outside developer mode");
            }
        }
    }

    private function readJson(string $file): ?array
    {
        $content = @file_get_contents($file);
        if ($content === false) {
            return null;
        }
        try {
            $json = json_decode($content, true, 512, JSON_THROW_ON_ERROR);
        } catch (JsonException $e) {
            $this->fail("$file is not valid JSON: " . $e->getMessage());
            return null;
        }
        if (!is_array($json)) {
            $this->fail("$file does not contain a JSON object");
            return null;
        }
        return $json;
    }

    private function validateTranslations(string $package, string $theme, string $locale, string $dir): void
    {
        $translations = $this->readJson("$dir/js-translation.json");
        if ($translations === null || $theme !== self::FIXTURE_THEME) {
            return;
        }
        $expected = [self::FIXTURE_PHRASE[0] => $locale === 'nl_NL' ? self::FIXTURE_PHRASE[1] : null];
        if ($locale === 'nl_NL') {
            $expected[self::DB_PHRASE[0]] = $this->options['db-translation'] ? self::DB_PHRASE[1] : null;
        }
        foreach ($expected as $phrase => $translation) {
            $actual = $translations[$phrase] ?? null;
            if ($actual !== $translation) {
                $this->fail(sprintf(
                    '%s/js-translation.json: "%s" should be %s, found %s',
                    $package,
                    $phrase,
                    $translation === null ? 'absent' : '"' . $translation . '"',
                    $actual === null ? 'nothing' : '"' . $actual . '"'
                ));
            }
        }
    }

    /** @return string[] problems in a CSS file */
    private function cssProblems(string $base, string $rel, bool $checkUrls): array
    {
        $css = @file_get_contents("$base/$rel");
        if ($css === false) {
            return [];
        }
        $problems = [];
        // {{base_url_path}} and {{locale}} are valid in email CSS (resolved when the email is sent)
        if (preg_match_all('/@magento_import|url\(\s*[\'"]?[A-Za-z0-9]+_[A-Za-z0-9]+::|@import[^;{}]*\.less/', $css, $m)) {
            foreach (array_unique($m[0]) as $match) {
                $problems[] = "unresolved $match";
            }
        }
        if ($checkUrls && preg_match_all('/url\(\s*([\'"]?)([^\'")]+)\1\s*\)/', $css, $m)) {
            foreach (array_unique($m[2]) as $url) {
                $url = trim($url);
                if ($url === '' || preg_match('#^(data:|[a-z]+://|//|\#|/|about:)#i', $url) || str_contains($url, '{{')) {
                    continue;
                }
                $path = preg_replace('/[?#].*$/', '', $url);
                if (!file_exists($base . '/' . dirname($rel) . '/' . $path)) {
                    $problems[] = "broken url($url)";
                }
            }
        }
        return $problems;
    }

    private function validateCss(string $package, string $dir, array $files): void
    {
        $checked = 0;
        foreach ($files as $rel) {
            if (!str_ends_with($rel, '.css')) {
                continue;
            }
            // Check the URLs of the compiled theme stylesheets (css/*.css)
            $checkUrls = (bool)preg_match('#^css/[^/]+\.css$#', $rel);
            $checked += $checkUrls ? 1 : 0;
            $problems = $this->cssProblems($dir, $rel, $checkUrls);
            if ($problems && $this->options['reference'] !== '') {
                $reference = $this->cssProblems($this->options['reference'] . '/' . $package, $rel, $checkUrls);
                $problems = array_diff($problems, $reference);
            }
            foreach ($problems as $problem) {
                $this->fail("$package/$rel: $problem");
            }
        }
        if ($checked === 0) {
            $this->fail("$package has no compiled css/*.css files");
        }
    }

    private function validateBundles(string $package, string $dir, array $files, array $set): void
    {
        $bundles = array_values(preg_grep('#^js/bundle/bundle\d+(\.min)?\.js$#', $files));
        if (!$bundles) {
            $this->fail("$package has no JavaScript bundles");
            return;
        }
        if ($this->options['minify'] && preg_grep('#\.min\.js$#', $bundles) !== $bundles) {
            $this->fail("$package has bundles without .min.js, while JS minification is enabled");
        }
        usort($bundles, fn($a, $b) => (int)preg_replace('/\D/', '', $a) <=> (int)preg_replace('/\D/', '', $b));

        $seen = [];
        $entries = 0;
        $trailers = [];
        foreach ($bundles as $rel) {
            $content = (string)file_get_contents("$dir/$rel");
            if (!preg_match('/^require\.config\(\{"config": \{\n        "(jsbuild|text)":/', $content, $m)) {
                $this->fail("$package/$rel does not start with a bundle pool");
                continue;
            }
            $pool = $m[1];
            $start = strlen($m[0]);
            $end = strpos($content, "\n}});\n", $start);
            if ($end === false) {
                $this->fail("$package/$rel has no end of the bundle pool");
                continue;
            }
            $json = json_decode(substr($content, $start, $end - $start), true);
            if (!is_array($json)) {
                $this->fail("$package/$rel does not contain a valid JSON pool");
                continue;
            }
            $rest = substr($content, $end + 6);
            if ($rest !== '') {
                if (!str_starts_with($rest, "require.config({\n    bundles: {\n        'mage/requirejs/static': [")) {
                    $this->fail("$package/$rel has unexpected content after the bundle pool");
                }
                $trailers[] = $rel;
            }
            foreach ($json as $key => $value) {
                $key = (string)$key;
                $entries++;
                if (isset($seen[$key])) {
                    $this->fail("$package: $key is in both {$seen[$key]} and $rel");
                    continue;
                }
                $seen[$key] = $rel;
                $ext = pathinfo($key, PATHINFO_EXTENSION);
                if (($pool === 'jsbuild') !== ($ext === 'js') || ($pool === 'text') !== ($ext === 'html')) {
                    $this->fail("$package/$rel: $key does not belong in the $pool pool");
                }
                if (!isset($set[$key])) {
                    $this->fail("$package/$rel: bundled file $key was not deployed");
                    continue;
                }
                $actual = (string)file_get_contents("$dir/$key");
                if (mb_detect_encoding($actual) !== 'UTF-8') {
                    $actual = mb_convert_encoding($actual, 'UTF-8');
                }
                if ($actual !== $value) {
                    $this->fail("$package/$rel: the bundled content of $key differs from the deployed file");
                }
                if (!str_contains($key, '.min.') && isset($set[substr($key, 0, -strlen($ext)) . 'min.' . $ext])) {
                    $this->fail("$package/$rel: $key is bundled while a .min.$ext version exists");
                }
            }
        }
        if ($trailers !== [end($bundles)]) {
            $this->fail("$package: the RequireJS bundles configuration should only be in the last bundle, found in: "
                . (implode(', ', $trailers) ?: 'none'));
        }
        if ($entries < 100) {
            $this->fail("$package: the bundles only contain $entries files");
        }
        echo '  bundles: ' . count($bundles) . " files, $entries entries\n";
    }

    private function validateSri(string $package, string $dir, array $files): void
    {
        $file = "$dir/sri-hashes.json";
        if (!$this->sri || $this->sriLayout === 'area') {
            if (file_exists($file)) {
                $this->fail("$package/sri-hashes.json exists, but " . ($this->sri
                    ? 'this Magento version writes <area>/sri-hashes.json'
                    : 'SRI is ' . ($this->options['no-sri'] ? 'disabled with --no-sri' : 'not installed')));
            }
            if (!$this->sri || is_link($dir)) {
                return;
            }
        }
        $expected = [];
        $optional = [];
        foreach ($files as $rel) {
            if (str_ends_with($rel, '.js')) {
                $expected["$package/$rel"] = 'sha256-' . base64_encode(hash_file('sha256', "$dir/$rel", true));
                if (preg_match('/^requirejs-min-resolver(\.min)?\.js$/', $rel)) {
                    $optional["$package/$rel"] = true;
                }
            }
        }
        if ($this->sriLayout === 'area') {
            $area = explode('/', $package)[0];
            $this->sriExpected[$area] = ($this->sriExpected[$area] ?? []) + $expected;
            $this->sriOptional[$area] = ($this->sriOptional[$area] ?? []) + $optional;
            return;
        }
        $this->validateSriFile($file, $expected, $optional);
    }

    /**
     * @param array<string, string> $expected hashes of all deployed JavaScript files
     * @param array<string, bool> $optional files that may be missing (Magento writes them after its post processors)
     */
    private function validateSriFile(string $file, array $expected, array $optional): void
    {
        $name = substr($file, strlen($this->static) + 1);
        $hashes = $this->readJson($file);
        if ($hashes === null) {
            $this->fail("$name is missing");
            return;
        }
        $malformed = array_keys(array_filter($hashes, fn($v) => !is_string($v) || !preg_match('#^sha256-[A-Za-z0-9+/]{43}=$#', $v)));
        if ($this->options['minify']) {
            // Magento lists minified files under their non-minified name, with the hash of the source file
            $normalize = fn(array $list) => array_combine(
                array_map(fn($k) => preg_replace('/\.min\.js$/', '.js', (string)$k), array_keys($list)),
                array_values($list)
            );
            $expected = $normalize($expected);
            $optional = $normalize($optional);
            $hashes = $normalize($hashes);
        }
        $missing = array_keys(array_diff_key($expected, $hashes, $optional));
        $extra = array_keys(array_diff_key($hashes, $expected));
        $wrong = $this->options['minify'] ? $malformed
            : array_merge($malformed, array_keys(array_diff_assoc(array_intersect_key($hashes, $expected), $expected)));
        foreach (['deployed JS files are missing in' => $missing, 'entries are not deployed JS files in' => $extra, 'wrong hashes in' => $wrong] as $what => $keys) {
            if ($keys) {
                $this->fail(sprintf('%d %s %s, e.g. %s', count($keys), $what, $name, implode(', ', array_slice($keys, 0, 5))));
            }
        }
        echo "  $name: " . count($hashes) . " hashes\n";
    }

    private function validateSymlinks(string $package, string $theme, string $locale, string $dir, array $files): void
    {
        $mode = $this->options['symlink'];
        if (is_link($dir)) {
            $target = (string)readlink($dir);
            if (!in_array($mode, ['locale', 'locale-contained'], true)) {
                $this->fail("$package is a symlink, but --symlink is '$mode'");
            } elseif (!in_array($target, $this->locales, true) || $target === $locale || is_link(dirname($dir) . "/$target")) {
                $this->fail("$package is a symlink to '$target', expected a deployed sibling locale");
            } elseif ($theme === self::FIXTURE_THEME) {
                $this->fail("$package is a symlink, but it has its own translations");
            } else {
                echo "  symlink to $target\n";
            }
            return;
        }

        $links = 0;
        foreach ($files as $rel) {
            if (!is_link("$dir/$rel")) {
                continue;
            }
            $links++;
            if (!in_array($mode, ['file', 'locale'], true)) {
                $this->fail("$package/$rel is a symlink, but --symlink is '$mode'");
                return;
            }
        }
        if (in_array($mode, ['file', 'locale'], true)) {
            if ($links < count($files) / 2) {
                $this->fail("$package: only $links of " . count($files) . " files are symlinks with --symlink $mode");
            }
            $generated = array_merge(
                ['js-translation.json', $this->generated('requirejs-config.js')],
                $this->sri ? ['sri-hashes.json'] : [],
                array_map([$this, 'generated'], $this->mainCss(explode('/', $package)[0], $theme)),
                preg_grep('#^js/bundle/#', $files)
            );
            foreach ($generated as $rel) {
                if (is_link("$dir/$rel")) {
                    $this->fail("$package/$rel is generated, but it is a symlink");
                }
            }
            echo "  $links symlinked files\n";
        }
    }

    private function validateFixture(string $package, string $dir, array $set): void
    {
        $fixtures = __DIR__ . '/fixtures/app/design/' . self::FIXTURE_THEME;
        foreach (['images/gsd-fixture.svg', 'Magento_Theme/gsd/marker.svg', 'Magento_Ui/templates/collection.html'] as $rel) {
            $source = $fixtures . '/' . preg_replace('#^((?:[A-Z][A-Za-z0-9]*_[A-Za-z0-9]+)/)?#', '$1web/', $rel);
            if (!isset($set[$rel])) {
                $this->fail("$package/$rel (fixture) is missing");
            } elseif (file_get_contents("$dir/$rel") !== file_get_contents($source)) {
                $this->fail("$package/$rel does not contain the fixture file");
            }
        }
        foreach ([$this->generated('js/gsd-fixture.js'), $this->generated('Magento_Theme/js/gsd-module.js')] as $rel) {
            if (!isset($set[$rel])) {
                $this->fail("$package/$rel (fixture) is missing");
            }
        }
        $requireJs = (string)@file_get_contents("$dir/" . $this->generated('requirejs-config.js'));
        if (!str_contains($requireJs, 'gsdFixture')) {
            $this->fail("$package/requirejs-config.js does not contain the fixture theme configuration");
        }
        $css = (string)@file_get_contents("$dir/" . $this->generated('css/styles-m.css'));
        foreach (['.gsd-fixture-marker', '../images/gsd-fixture.svg', '.gsd-fixture-module-url', 'Magento_Theme/gsd/marker.svg', '.gsd-fixture-module-marker'] as $needle) {
            if (!str_contains($css, $needle)) {
                $this->fail("$package/css/styles-m.css does not contain $needle (fixture LESS)");
            }
        }
        if (str_contains($css, 'Magento_Theme::')) {
            $this->fail("$package/css/styles-m.css contains an unresolved Magento_Theme:: URL");
        }
        if (!preg_match('/\.gsd-fixture-marker\s*\{[^}]*color:\s*#[0-9a-f]{3,6}\b/i', $css)) {
            $this->fail("$package/css/styles-m.css: the LESS variable @primary__color was not resolved for .gsd-fixture-marker");
        }
    }

    private function validateNoFixture(string $package, string $dir): void
    {
        $css = (string)@file_get_contents("$dir/" . $this->generated('css/styles-m.css'));
        $template = (string)@file_get_contents("$dir/Magento_Ui/templates/collection.html");
        if (str_contains($css, 'gsd-fixture') || str_contains($template, 'gsd-fixture')) {
            $this->fail("$package contains fixture theme content (theme inheritance is reversed)");
        }
    }
}

$options = ['mode' => 'default', 'minify' => false, 'no-sri' => false, 'symlink' => '', 'reference' => '', 'db-translation' => false];
$args = [];
foreach (array_slice($argv, 1) as $arg) {
    if (preg_match('/^--([a-z-]+)(?:=(.*))?$/', $arg, $m)) {
        if (!array_key_exists($m[1], $options) || is_bool($options[$m[1]]) === isset($m[2])) {
            fwrite(STDERR, "Invalid option $arg\n");
            exit(2);
        }
        $options[$m[1]] = $m[2] ?? true;
        continue;
    }
    $args[] = $arg;
}
if (count($args) < 2) {
    fwrite(STDERR, "Usage: php {$argv[0]} [options] <magento-root> <locale>...\n");
    exit(2);
}
$root = rtrim(array_shift($args), '/');
exit((new StaticValidator($root, $args, $options))->run());
