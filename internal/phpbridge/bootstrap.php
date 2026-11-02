<?php
/**
 * PHP helper for go-static-deploy.
 *
 * This script is embedded in the Go binary and written to <magento-root>/var/.go-static-deploy/
 * at runtime. It never bootstraps the full Magento application. It only loads the Composer
 * autoloader so that a few PHP-only pieces can be reused:
 *
 *  - component registration (registration.php files) and app/etc/{config,env}.php
 *  - LESS compilation with wikimedia/less.php (same library and options as Magento)
 *  - CSS url() rewriting using Magento's own CssResolver (PCRE-specific regular expressions)
 *  - JS translation dictionaries (fgetcsv, PHP array semantics, intl MessageFormatter, json_encode)
 *  - phtml template minification using Magento's own HTML minifier
 *  - optional: reading system configuration and translations from the database (PDO only)
 *
 * Usage:
 *   php bootstrap.php <magento-root> config [--with-db]
 *   php bootstrap.php <magento-root> worker [--with-db]
 */

declare(strict_types=1);

error_reporting(E_ALL & ~E_DEPRECATED & ~E_USER_DEPRECATED);
ini_set('display_errors', 'stderr');
ini_set('memory_limit', '-1');

if ($argc < 3) {
    fwrite(STDERR, "Usage: php bootstrap.php <magento-root> <config|worker> [--with-db]\n");
    exit(2);
}

$root = rtrim($argv[1], '/');
$command = $argv[2];
$withDb = in_array('--with-db', array_slice($argv, 3), true);

if (!defined('BP')) {
    define('BP', $root);
}

chdir($root);

$vendorDir = $root . '/vendor';
if (is_file($root . '/app/etc/vendor_path.php')) {
    $vendorPath = require $root . '/app/etc/vendor_path.php';
    $vendorDir = $root . '/' . ltrim((string)$vendorPath, './');
    if (!is_dir($vendorDir)) {
        $vendorDir = $root . '/vendor';
    }
}

/** @noinspection PhpIncludeInspection */
require $vendorDir . '/autoload.php';

if (is_file($root . '/app/etc/NonComposerComponentRegistration.php')) {
    require_once $root . '/app/etc/NonComposerComponentRegistration.php';
}

/**
 * Small helpers
 */
final class GsdUtil
{
    public static function loadPhpArray(string $file): array
    {
        if (!is_file($file)) {
            return [];
        }
        $data = include $file;
        return is_array($data) ? $data : [];
    }

    public static function flatten(array $data, string $prefix = ''): array
    {
        $result = [];
        foreach ($data as $key => $value) {
            $path = $prefix === '' ? (string)$key : $prefix . '/' . $key;
            if (is_array($value)) {
                $result += self::flatten($value, $path);
            } else {
                $result[$path] = $value === null ? null : (is_bool($value) ? ($value ? '1' : '0') : (string)$value);
            }
        }
        return $result;
    }
}

/**
 * Database access (PDO only, no Magento bootstrap)
 */
final class GsdDb
{
    private static ?PDO $pdo = null;
    private static string $prefix = '';

    public static function connect(string $root): ?PDO
    {
        if (self::$pdo !== null) {
            return self::$pdo;
        }
        $env = GsdUtil::loadPhpArray($root . '/app/etc/env.php');
        $conn = $env['db']['connection']['default'] ?? null;
        if (!$conn) {
            throw new RuntimeException('No default DB connection found in app/etc/env.php');
        }
        self::$prefix = (string)($env['db']['table_prefix'] ?? '');
        $host = (string)($conn['host'] ?? 'localhost');
        $dsn = 'mysql:dbname=' . $conn['dbname'] . ';charset=utf8';
        if (str_contains($host, '/')) {
            $dsn .= ';unix_socket=' . $host;
        } elseif (str_contains($host, ':')) {
            [$h, $p] = explode(':', $host, 2);
            $dsn .= ';host=' . $h . (is_numeric($p) ? ';port=' . $p : ';unix_socket=' . $p);
        } else {
            $dsn .= ';host=' . $host;
        }
        self::$pdo = new PDO($dsn, (string)($conn['username'] ?? ''), (string)($conn['password'] ?? ''), [
            PDO::ATTR_ERRMODE => PDO::ERRMODE_EXCEPTION,
        ]);
        return self::$pdo;
    }

    public static function table(string $name): string
    {
        return self::$prefix . $name;
    }

    public static function defaultConfig(string $root): array
    {
        $pdo = self::connect($root);
        $stmt = $pdo->query(
            'SELECT path, value FROM ' . self::table('core_config_data') . " WHERE scope = 'default' AND scope_id = 0 AND path LIKE 'dev/%'"
        );
        $result = [];
        foreach ($stmt->fetchAll(PDO::FETCH_ASSOC) as $row) {
            $result[$row['path']] = $row['value'];
        }
        return $result;
    }

    public static function defaultStoreId(string $root): int
    {
        $pdo = self::connect($root);
        $sql = 'SELECT g.default_store_id FROM ' . self::table('store_website') . ' w'
            . ' JOIN ' . self::table('store_group') . ' g ON g.group_id = w.default_group_id'
            . ' WHERE w.is_default = 1 LIMIT 1';
        $value = $pdo->query($sql)->fetchColumn();
        return $value === false ? 1 : (int)$value;
    }

    public static function translations(string $root, string $locale, array $storeIds): array
    {
        $pdo = self::connect($root);
        $placeholders = implode(',', array_fill(0, count($storeIds), '?'));
        $stmt = $pdo->prepare(
            'SELECT string, translate FROM ' . self::table('translation')
            . " WHERE locale = ? AND store_id IN ($placeholders) ORDER BY store_id"
        );
        $stmt->execute(array_merge([$locale], $storeIds));
        $result = [];
        foreach ($stmt->fetchAll(PDO::FETCH_ASSOC) as $row) {
            $result[$row['string']] = $row['translate'];
        }
        return $result;
    }
}

/**
 * `config` command: component registration + deployment configuration
 */
function gsd_config(string $root, bool $withDb): array
{
    $registrar = new \Magento\Framework\Component\ComponentRegistrar();
    $components = [];
    foreach (['module', 'theme', 'language', 'library'] as $type) {
        $components[$type] = [];
        foreach ($registrar->getPaths($type) as $name => $path) {
            $components[$type][] = ['name' => (string)$name, 'path' => str_replace('\\', '/', (string)$path)];
        }
    }

    $config = GsdUtil::loadPhpArray($root . '/app/etc/config.php');
    $env = GsdUtil::loadPhpArray($root . '/app/etc/env.php');

    $modules = [];
    foreach (($config['modules'] ?? []) as $name => $enabled) {
        $modules[] = ['name' => (string)$name, 'enabled' => (bool)$enabled];
    }

    $system = array_replace_recursive($config['system'] ?? [], $env['system'] ?? []);
    $systemDefault = GsdUtil::flatten((array)($system['default'] ?? []));

    $result = [
        'php_version' => PHP_VERSION,
        'mode' => (string)($env['MAGE_MODE'] ?? (getenv('MAGE_MODE') ?: 'default')),
        'modules' => $modules,
        'components' => $components,
        'system_default' => (object)$systemDefault,
        'db_default' => new stdClass(),
        'static_content_only_enabled_modules' => (bool)($env['static_content_only_enabled_modules']
            ?? $config['static_content_only_enabled_modules'] ?? false),
        'has_less_php' => class_exists('Less_Parser'),
        'has_intl' => class_exists('MessageFormatter'),
    ];

    if ($withDb) {
        $result['db_default'] = (object)GsdDb::defaultConfig($root);
        $result['default_store_id'] = GsdDb::defaultStoreId($root);
    }

    return $result;
}

/**
 * Worker: line based JSON protocol on STDIN/STDOUT
 */
final class GsdWorker
{
    private string $root;
    private bool $withDb;
    private array $phraseCache = [];
    private array $packCache = [];
    private ?object $cssResolver = null;
    private ?object $htmlMinifier = null;

    public function __construct(string $root, bool $withDb)
    {
        $this->root = $root;
        $this->withDb = $withDb;
    }

    public function run(): void
    {
        while (($line = fgets(STDIN)) !== false) {
            $line = trim($line);
            if ($line === '') {
                continue;
            }
            $request = json_decode($line, true);
            $id = $request['id'] ?? 0;
            try {
                $result = $this->handle($request);
                $response = ['id' => $id, 'ok' => true] + $result;
            } catch (\Throwable $e) {
                $response = ['id' => $id, 'ok' => false, 'error' => $e->getMessage()];
            }
            $json = json_encode($response, JSON_INVALID_UTF8_SUBSTITUTE | JSON_UNESCAPED_SLASHES);
            if ($json === false) {
                $json = json_encode(['id' => $id, 'ok' => false, 'error' => json_last_error_msg()]);
            }
            fwrite(STDOUT, $json . "\n");
            fflush(STDOUT);
        }
    }

    private function handle(array $request): array
    {
        switch ($request['cmd'] ?? '') {
            case 'ping':
                return ['pong' => true];
            case 'less':
                return $this->compileLess($request);
            case 'css':
                return ['content' => $this->processCss((string)$request['content'], $request)];
            case 'i18n':
                return ['content' => $this->buildJsTranslation($request)];
            case 'minify-templates':
                return ['count' => $this->minifyTemplates((array)$request['files'])];
            case 'bundle':
                return ['count' => $this->bundle($request)];
            case 'view-config':
                return ['content' => json_encode($this->viewConfig($request))];
            default:
                throw new InvalidArgumentException('Unknown command: ' . ($request['cmd'] ?? ''));
        }
    }

    /**
     * Same as \Magento\Framework\Css\PreProcessor\Adapter\Less\Processor::processContent()
     */
    private function compileLess(array $request): array
    {
        $mode = (string)($request['mode'] ?? 'production');
        $developer = $mode === 'developer';
        $parser = new \Less_Parser([
            'relativeUrls' => false,
            'compress' => !$developer,
            'sourceMap' => $developer,
            'sourceMapRootpath' => '/',
            'sourceMapBasepath' => $this->root . '/var/view_preprocessed/pub/',
        ]);
        $file = (string)$request['file'];
        if (trim((string)file_get_contents($file)) === '') {
            throw new RuntimeException('Compilation from source: LESS file is empty: ' . $request['asset_path']);
        }
        gc_disable();
        try {
            $parser->parseFile($file, '');
            $content = $parser->getCss();
        } finally {
            gc_enable();
        }
        if (trim($content) === '') {
            throw new RuntimeException('Compilation from source: LESS file is empty: ' . $request['asset_path']);
        }
        return ['content' => $this->processCss($content, $request)];
    }

    private function getCssResolver(): object
    {
        if ($this->cssResolver === null) {
            $this->cssResolver = new \Magento\Framework\View\Url\CssResolver();
        }
        return $this->cssResolver;
    }

    /**
     * Same as the VariableNotation and ModuleNotation CSS pre-processors of Magento
     */
    private function processCss(string $content, array $request): string
    {
        $area = (string)$request['area'];
        $themePath = (string)$request['theme'];
        $thisPath = (string)$request['context_path'] . '/' . (string)$request['asset_path'];
        $contextPath = (string)$request['context_path'];
        $resolver = $this->getCssResolver();

        $content = $resolver->replaceRelativeUrls($content, function ($path) use ($area, $themePath) {
            if (!preg_match_all('/{{([_a-z]*)}}/si', (string)$path, $matches, PREG_SET_ORDER)) {
                return $path;
            }
            $replacements = [];
            foreach ($matches as $match) {
                if (!isset($replacements[$match[0]])) {
                    $replacements[$match[0]] = $match[1] === 'base_url_path'
                        ? '{{base_url_path}}' . $area . ($themePath ? '/' . $themePath . '/' : '') . '{{locale}}'
                        : '';
                }
            }
            return str_replace(array_keys($replacements), $replacements, (string)$path);
        });

        return $resolver->replaceRelativeUrls($content, function ($path) use ($thisPath, $contextPath) {
            if (!$path || !str_contains((string)$path, '::')) {
                return $path;
            }
            [$module, $filePath] = explode('::', (string)$path, 2);
            $relatedPath = $contextPath . '/' . $module . '/' . $filePath;
            $offset = \Magento\Framework\View\FileSystem::offsetPath($relatedPath, $thisPath);
            return \Magento\Framework\View\FileSystem::normalizePath($offset . '/' . basename($relatedPath));
        });
    }

    /**
     * Same as \Magento\Translation\Model\Js\DataProvider::getData() combined with
     * \Magento\Framework\Translate::loadData()
     */
    private function buildJsTranslation(array $request): string
    {
        $locale = (string)$request['locale'];
        $area = (string)$request['area'];

        $data = [];
        foreach ((array)$request['module_csvs'] as $file) {
            $this->addData($data, $this->getCsvDataPairs((string)$file));
        }
        $this->addData($data, $this->getPackDictionary($locale, (array)$request['language_packs']));
        foreach ((array)$request['theme_csvs'] as $file) {
            if ($file) {
                $this->addData($data, $this->getCsvDataPairs((string)$file));
            }
        }
        if (!empty($request['with_db'])) {
            $dbData = GsdDb::translations($this->root, $locale, array_map('intval', (array)$request['store_ids']));
            $this->addData($data, array_map('htmlspecialchars_decode', $dbData));
        }

        $phrases = $this->getPhrases($area, (array)$request['phrase_files'], (array)$request['patterns']);
        $dictionary = [];
        foreach ($phrases as $phrase) {
            $translated = $this->renderPhrase((string)$phrase, $data, $locale);
            if ($phrase != $translated) {
                $dictionary[$phrase] = $translated;
            }
        }
        ksort($dictionary);
        return json_encode($dictionary);
    }

    private function renderPhrase(string $phrase, array $data, string $locale): string
    {
        $text = strtr($phrase, ['\"' => '"', "\\'" => "'"]);
        $text = array_key_exists($text, $data) ? $data[$text] : $phrase;
        $text = (string)$text;
        if (strpos($text, '{') === false || !class_exists('MessageFormatter')) {
            return $text;
        }
        $result = \MessageFormatter::formatMessage($locale, $text, []);
        return $result !== false ? $result : $text;
    }

    private function getPhrases(string $area, array $files, array $patterns): array
    {
        if (isset($this->phraseCache[$area])) {
            return $this->phraseCache[$area];
        }
        if (!$patterns) {
            $patterns = [
                '~(?:i18n\:|_\.i18n\()\s*(["\'])(.*?)(?<!\\\\)\1~',
                '~translate\=("\')([^\'].*?)\'\"~',
                '~(?s)(?:\$|jQuery)\.mage\.__\(\s*([\'"])(?<translate>.+?)(?<!\\\\)\1\s*(*SKIP)\)\s*(?s)~',
                '~(?s)\$t\(\s*([\'"])(?<translate>.+?)(?<!\\\\)\1\s*(*SKIP)\)(?s)~',
                '~translate args\=("|\'|"\'|\\\\"\')([^\'].*?)(\'\\\\"|\'"|\'|")~',
            ];
        }
        $phrases = [];
        foreach ($files as $file) {
            $content = @file_get_contents((string)$file);
            if ($content === false) {
                continue;
            }
            foreach ($patterns as $pattern) {
                $concatenatedContent = preg_replace('~(["\'])\s*?\+\s*?\1~', '', $content);
                $result = preg_match_all($pattern, $concatenatedContent, $matches);
                if ($result && isset($matches[2])) {
                    foreach ($matches[2] as $match) {
                        $phrases[] = $match !== null ? str_replace(["\'", '\"'], ["'", '"'], $match) : '';
                    }
                }
                if ($result === false) {
                    throw new RuntimeException('Error while generating js translation dictionary in ' . $file);
                }
            }
        }
        $this->phraseCache[$area] = $phrases;
        return $phrases;
    }

    /**
     * Same as \Magento\Framework\Translate::_addData()
     */
    private function addData(array &$target, array $data): void
    {
        foreach ($data as $key => $value) {
            if ($key === $value) {
                if (isset($target[$key])) {
                    unset($target[$key]);
                }
                continue;
            }
            $key = (string)$key;
            $value = (string)$value;
            $key = str_replace('""', '"', $key);
            $value = str_replace('""', '"', $value);
            $target[$key] = $value;
        }
    }

    /**
     * Same as \Magento\Framework\File\Csv::getDataPairs()
     */
    private function getCsvDataPairs(string $file): array
    {
        $data = [];
        if (!is_file($file)) {
            return $data;
        }
        $fh = fopen($file, 'r');
        while ($row = fgetcsv($fh, 0, ',', '"', "\0")) {
            if (isset($row[0])) {
                $data[$row[0]] = $row[1] ?? null;
            }
        }
        fclose($fh);
        return $data;
    }

    /**
     * Same as \Magento\Framework\App\Language\Dictionary::getDictionary()
     */
    private function getPackDictionary(string $locale, array $packPaths): array
    {
        if (isset($this->packCache[$locale])) {
            return $this->packCache[$locale];
        }
        $packList = [];
        $languages = [];
        foreach ($packPaths as $path) {
            $file = rtrim((string)$path, '/') . '/language.xml';
            if (!is_file($file)) {
                continue;
            }
            $xml = simplexml_load_file($file);
            if ($xml === false) {
                continue;
            }
            $uses = [];
            foreach ($xml->use ?? [] as $use) {
                $uses[] = ['vendor' => (string)$use['vendor'], 'package' => (string)$use['package']];
            }
            $config = [
                'code' => (string)$xml->code,
                'vendor' => (string)$xml->vendor,
                'package' => (string)$xml->package,
                'sort_order' => (int)(string)($xml->sort_order ?? 0),
                'uses' => $uses,
                'path' => rtrim((string)$path, '/'),
            ];
            $packList[$config['vendor']][$config['package']] = $config;
            if ($config['code'] === $locale) {
                $languages[] = $config;
            }
        }

        $packs = [];
        foreach ($languages as $language) {
            $visited = [];
            $this->collectInheritedPacks($packList, $language, $packs, 0, $visited);
        }

        $firstLevel = array_filter($packs, fn($pack) => $pack['inheritance_level'] === 0);
        uasort($firstLevel, function ($current, $next) {
            if ($current['sort_order'] > $next['sort_order']) {
                return -1;
            } elseif ($current['sort_order'] < $next['sort_order']) {
                return 1;
            }
            return strcmp($next['key'], $current['key']);
        });
        $sorted = [];
        foreach ($firstLevel as $pack) {
            $this->addInheritedPacks($packs, $pack, $sorted);
        }
        $sorted = array_reverse($sorted, true);

        $result = [];
        foreach ($sorted as $packInfo) {
            $path = $packInfo['language']['path'];
            $csvFiles = glob($path . '/*.csv') ?: [];
            foreach ($csvFiles as $csvFile) {
                $fh = fopen($csvFile, 'r');
                while (($row = fgetcsv($fh, 0, ',', '"', "\0")) !== false) {
                    if (is_array($row) && count($row) > 1) {
                        $result[$row[0]] = $row[1];
                    }
                }
                fclose($fh);
            }
        }
        $this->packCache[$locale] = $result;
        return $result;
    }

    private function collectInheritedPacks(array $packList, array $language, array &$result, int $level, array &$visited): void
    {
        $packKey = $language['vendor'] . '|' . $language['package'];
        if (!isset($visited[$packKey])
            && (!isset($result[$packKey]) || $result[$packKey]['inheritance_level'] < $level)) {
            $visited[$packKey] = true;
            $result[$packKey] = [
                'inheritance_level' => $level,
                'sort_order' => $language['sort_order'],
                'language' => $language,
                'key' => $packKey,
            ];
            foreach ($language['uses'] as $reuse) {
                if (isset($packList[$reuse['vendor']][$reuse['package']])) {
                    $this->collectInheritedPacks(
                        $packList,
                        $packList[$reuse['vendor']][$reuse['package']],
                        $result,
                        $level + 1,
                        $visited
                    );
                }
            }
        }
    }

    private function addInheritedPacks(array $packs, array $pack, array &$sorted): void
    {
        if (isset($sorted[$pack['key']])) {
            return;
        }
        $sorted[$pack['key']] = $pack;
        foreach ($pack['language']['uses'] as $reuse) {
            $packKey = $reuse['vendor'] . '|' . $reuse['package'];
            if (isset($packs[$packKey])) {
                $this->addInheritedPacks($packs, $packs[$packKey], $sorted);
            }
        }
    }

    /**
     * Reads the merged view.xml configuration of a theme with Magento's own \Magento\Framework\Config\View
     * reader. Only the JS bundling settings are returned.
     */
    private function viewConfig(array $request): array
    {
        $read = function (array $files): array {
            $result = [];
            foreach ($files as $key => $file) {
                if ($file && is_file((string)$file)) {
                    // Media entries are irrelevant for deployment and need extra merge configuration
                    $dom = new DOMDocument();
                    $dom->loadXML((string)file_get_contents((string)$file));
                    foreach (iterator_to_array($dom->getElementsByTagName('media')) as $media) {
                        $media->parentNode->removeChild($media);
                    }
                    $result[$key] = $dom->saveXML();
                }
            }
            return $result;
        };
        $global = $read(array_merge((array)$request['module_files'], [(string)$request['theme_file']]));
        $parents = $request['theme_file'] ? $read((array)$request['parent_files']) : $read((array)$request['module_files']);

        $fileResolver = new class($global, $parents) implements \Magento\Framework\Config\FileResolverInterface {
            public function __construct(private array $global, private array $parents)
            {
            }

            public function get($filename, $scope)
            {
                return $this->global;
            }

            public function getParents($filename, $scope)
            {
                return $this->parents;
            }
        };
        $schemaLocator = new class implements \Magento\Framework\Config\SchemaLocatorInterface {
            public function getSchema()
            {
                return null;
            }

            public function getPerFileSchema()
            {
                return null;
            }
        };
        $pool = (new ReflectionClass(\Magento\Framework\View\Xsd\Media\TypeDataExtractorPool::class))
            ->newInstanceWithoutConstructor();
        $converter = new class($pool) extends \Magento\Framework\Config\Converter {
            public function convert($source)
            {
                foreach (iterator_to_array($source->getElementsByTagName('media')) as $media) {
                    $media->parentNode->removeChild($media);
                }
                return parent::convert($source);
            }
        };
        $view = new \Magento\Framework\Config\View(
            $fileResolver,
            $converter,
            $schemaLocator,
            new \Magento\Framework\App\Arguments\ValidationState('production'),
            'view.xml'
        );
        return [
            'bundle_size' => (string)$view->getVarValue('Js_Bundle', 'bundle_size'),
            'excluded_files' => array_values($view->getExcludedFiles()),
            'excluded_dirs' => array_values($view->getExcludedDir()),
        ];
    }

    /**
     * Same as \Magento\Deploy\Service\Bundle::deploy() with \Magento\Deploy\Package\Bundle\RequireJs
     */
    private function bundle(array $request): int
    {
        $staticDir = $this->root . '/pub/static';
        $area = (string)$request['area'];
        $theme = (string)$request['theme'];
        $locale = (string)$request['locale'];
        $packagePath = $area . '/' . $theme . '/' . $locale;
        $packageDir = $staticDir . '/' . $packagePath;
        $bundleDir = $packagePath . '/js/bundle';
        $jsMinify = !empty($request['js_minify']);
        $jsExcludes = (array)($request['js_minify_exclude'] ?? []);

        $addMinifiedSign = function (string $filename) use ($jsMinify, $jsExcludes): string {
            $extension = pathinfo($filename, PATHINFO_EXTENSION);
            if ($extension !== 'js' || !$jsMinify) {
                return $filename;
            }
            foreach ($jsExcludes as $exclude) {
                if (preg_match('/' . str_replace('/', '\/', (string)$exclude) . '/', $filename)) {
                    return $filename;
                }
            }
            if (substr($filename, strrpos($filename, '.') - 4, 5) == '.min.') {
                return $filename;
            }
            return substr($filename, 0, -strlen($extension)) . 'min.' . $extension;
        };

        $deleteDir = function (string $dir) use (&$deleteDir): void {
            if (!is_dir($dir) || is_link($dir)) {
                return;
            }
            foreach (scandir($dir) as $entry) {
                if ($entry === '.' || $entry === '..') {
                    continue;
                }
                $path = $dir . '/' . $entry;
                is_dir($path) && !is_link($path) ? $deleteDir($path) : unlink($path);
            }
            rmdir($dir);
        };
        $deleteDir($staticDir . '/' . $bundleDir);

        $size = (string)$request['bundle_size'];
        $unit = strtoupper(preg_replace('/[^a-zA-Z]+/', '', $size));
        switch ($unit) {
            case 'KB':
                $maxSize = (int)$size;
                break;
            case 'MB':
                $maxSize = (int)$size * 1024;
                break;
            default:
                $maxSize = (int)((int)$size / 1024);
        }

        $prepareExcludePath = function ($path) {
            if ($path !== null && strpos($path, '::') !== false) {
                [$module, $file] = explode('::', $path);
                return $module == 'Lib' ? $file : $module . '/' . $file;
            }
            return $path;
        };
        $excludedFiles = array_map($prepareExcludePath, (array)$request['excluded_files']);
        $excludedDirs = array_map($prepareExcludePath, (array)$request['excluded_dirs']);

        // Magento skips file.js when file.min.js exists. Its implementation depends on the (unsorted)
        // filesystem order and on previously deployed packages. Here the intended behaviour is applied
        // deterministically: all .min. files are collected first.
        $pools = ['jsbuild' => [], 'text' => []];
        $files = [];
        $excludedCache = [];
        foreach (\Magento\Framework\App\Utility\Files::getFiles([$packageDir], '*.*') as $sourcePath) {
            $sourcePath = str_replace('\\', '/', (string)$sourcePath);
            $relative = substr($sourcePath, strlen($staticDir) + 1);
            $filePath = substr($relative, strlen($packagePath) + 1);
            $files[$filePath] = $relative;
            $info = pathinfo($filePath);
            if (isset($info['extension']) && strpos($filePath, '.min.') !== false) {
                $excludedCache[str_replace('.min.' . $info['extension'], '.' . $info['extension'], $filePath)] = true;
            }
        }
        foreach ($files as $filePath => $relative) {
            $filePath = (string)$filePath;
            $info = pathinfo($filePath);
            if (!isset($info['extension']) || !in_array($info['extension'], ['js', 'html'], true)) {
                continue;
            }
            if (isset($excludedCache[$filePath])) {
                continue;
            }
            if (strpos($filePath, '.min.') === false) {
                $minVersion = $info['dirname'] . '/' . $info['filename'] . '.min.' . $info['extension'];
                if (file_exists($staticDir . '/' . $minVersion)) {
                    continue;
                }
            }
            $excluded = false;
            foreach ($excludedFiles as $excludedFile) {
                if ($excludedFile === $filePath || $excludedFile === str_replace('.min.js', '.js', $filePath)) {
                    $excluded = true;
                    break;
                }
            }
            if (!$excluded) {
                foreach ($excludedDirs as $excludedDir) {
                    if (strpos($filePath, (string)$excludedDir) === 0) {
                        $excluded = true;
                        break;
                    }
                }
            }
            if ($excluded) {
                continue;
            }
            $pools[$info['extension'] === 'js' ? 'jsbuild' : 'text'][$filePath] = $relative;
        }

        $index = 0;
        $handle = null;
        $count = 0;
        $start = function (string $poolName) use (&$index, &$handle, &$count, $staticDir, $bundleDir, $addMinifiedSign) {
            $file = $staticDir . '/' . $addMinifiedSign($bundleDir . '/bundle' . $index . '.js');
            if (!is_dir(dirname($file))) {
                mkdir(dirname($file), 0777, true);
            }
            $handle = fopen($file, 'w');
            fwrite($handle, "require.config({\"config\": {\n");
            fwrite($handle, "        \"{$poolName}\":");
            ++$index;
            ++$count;
        };
        $end = function (array $contents) use (&$handle) {
            if ($contents) {
                fwrite($handle, json_encode($contents, JSON_UNESCAPED_SLASHES) . "\n");
            } else {
                fwrite($handle, "{}\n");
            }
            fwrite($handle, "}});\n");
        };
        $contentCache = [];
        foreach ($pools as $poolName => $files) {
            if (empty($files)) {
                continue;
            }
            $content = [];
            $freeSpace = $maxSize;
            if ($handle) {
                fclose($handle);
            }
            $start($poolName);
            foreach ($files as $filePath => $sourcePath) {
                if (!isset($contentCache[$sourcePath])) {
                    $fileContent = (string)file_get_contents($staticDir . '/' . $addMinifiedSign($sourcePath));
                    if (mb_detect_encoding($fileContent) !== 'UTF-8') {
                        $fileContent = mb_convert_encoding($fileContent, 'UTF-8');
                    }
                    $contentCache[$sourcePath] = $fileContent;
                }
                $fileContent = $contentCache[$sourcePath];
                $size = mb_strlen($fileContent, 'utf-8') / 1024;
                if ($freeSpace > $size) {
                    $freeSpace -= $size;
                    $content[$addMinifiedSign($filePath)] = $fileContent;
                } else {
                    $end($content);
                    fclose($handle);
                    $freeSpace = $maxSize - $size;
                    $content = [$addMinifiedSign($filePath) => $fileContent];
                    $start($poolName);
                }
            }
            $end($content);
        }
        if ($handle) {
            fwrite($handle, "require.config({\n" .
                "    bundles: {\n" .
                "        'mage/requirejs/static': [\n" .
                "            'jsbuild',\n" .
                "            'buildTools',\n" .
                "            'text',\n" .
                "            'statistician'\n" .
                "        ]\n" .
                "    },\n" .
                "    deps: [\n" .
                "        'jsbuild'\n" .
                "    ]\n" .
                "});\n");
            fclose($handle);
        }
        return $count;
    }

    /**
     * Same as \Magento\Deploy\Service\MinifyTemplates::minifyTemplates()
     */
    private function minifyTemplates(array $files): int
    {
        if ($this->htmlMinifier === null) {
            $driverPool = new \Magento\Framework\Filesystem\DriverPool();
            $directoryList = new \Magento\Framework\App\Filesystem\DirectoryList($this->root);
            $readFactory = new \Magento\Framework\Filesystem\Directory\ReadFactory($driverPool);
            $filesystem = new \Magento\Framework\Filesystem(
                $directoryList,
                $readFactory,
                new \Magento\Framework\Filesystem\Directory\WriteFactory($driverPool)
            );
            $this->htmlMinifier = new \Magento\Framework\View\Template\Html\Minifier($filesystem, $readFactory);
        }
        $count = 0;
        foreach ($files as $file) {
            $this->htmlMinifier->minify((string)$file);
            $count++;
        }
        return $count;
    }
}

switch ($command) {
    case 'config':
        echo json_encode(gsd_config($root, $withDb), JSON_UNESCAPED_SLASHES | JSON_INVALID_UTF8_SUBSTITUTE), "\n";
        break;
    case 'worker':
        (new GsdWorker($root, $withDb))->run();
        break;
    default:
        fwrite(STDERR, "Unknown command: $command\n");
        exit(2);
}
