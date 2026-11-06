#!/usr/bin/env bash
#
# Compare two pub/static trees, for instance the output of bin/magento setup:static-content:deploy
# (copied to another directory) with the output of go-static-deploy.
#
# Usage: scripts/compare.sh [--minified] <native-pub-static> <go-pub-static> [package-path...]
#   --minified    JS/CSS minification was enabled: the contents of .min.js/.min.css files, their SRI hashes
#                 and the bundled contents are not compared (a different minifier is used), only their names.
#   package-path  e.g. frontend/Magento/luma/en_US (default: all packages found in both directories)
#
# JSON files (js-translation.json, sri-hashes.json) are compared semantically. JS bundles (js/bundle/*.js) are
# compared by their merged contents, because Magento fills them in (unsorted) filesystem order. For the same
# reason the hashes of bundles in sri-hashes.json are ignored. Bundle entries file.js for which file.min.js
# exists are ignored: Magento adds a few of them to the first package of an area (see README.md).

set -u

minified=0
if [ "${1:-}" = "--minified" ]; then
    minified=1
    shift
fi

php_compare_json='
$minified = $argv[3] === "1";
$a = json_decode(file_get_contents($argv[1]), true);
$b = json_decode(file_get_contents($argv[2]), true);
if (str_ends_with($argv[1], "sri-hashes.json")) {
    if (!is_array($a) || !is_array($b)) {
        exit(1);
    }
    // Bundles are left out (the number of bundle files can differ), the hashes of minified files are not compared
    $normalize = function (array $hashes) use ($minified): array {
        $result = [];
        foreach ($hashes as $key => $value) {
            if (!str_contains($key, "/js/bundle/")) {
                $result[$key] = $minified && str_contains($key, ".min.") ? "" : $value;
            }
        }
        return $result;
    };
    $a = $normalize($a);
    $b = $normalize($b);
    if ($a != $b) {
        // Print a few differing keys, to explain the difference
        foreach ([
            "only in native" => array_keys(array_diff_key($a, $b)),
            "only in go-static-deploy" => array_keys(array_diff_key($b, $a)),
            "different hash" => array_keys(array_diff_assoc(array_intersect_key($a, $b), $b)),
        ] as $what => $keys) {
            if ($keys) {
                echo "    $what (", count($keys), "): ", implode(", ", array_slice($keys, 0, 5)), "\n";
            }
        }
    }
}
exit($a == $b ? 0 : 1);'

# Prints "<hash> <entries> <ignored>" for the merged contents of the bundles of a package.
php_bundle_contents='
$dir = $argv[1];
$minified = $argv[2] === "1";
$result = [];
$ignored = 0;
$prefix = "require.config({\"config\": {\n        \"";
foreach (glob($dir . "/js/bundle/*.js") as $file) {
    $content = file_get_contents($file);
    if (!str_starts_with($content, $prefix)) {
        continue;
    }
    $content = substr($content, strlen($prefix));
    $pool = substr($content, 0, strpos($content, "\""));
    $json = substr($content, strlen($pool) + 2, strpos($content, "\n}});\n") - strlen($pool) - 2);
    foreach (json_decode($json, true) ?? [] as $key => $value) {
        $ext = pathinfo((string)$key, PATHINFO_EXTENSION);
        if (!str_contains((string)$key, ".min.") && is_file($dir . "/" . substr((string)$key, 0, -strlen($ext)) . "min." . $ext)) {
            $ignored++;
            continue;
        }
        $result[$pool][$key] = $minified ? "" : $value;
    }
}
foreach ($result as &$pool) {
    ksort($pool);
}
echo md5(serialize($result)), " ", array_sum(array_map("count", $result)), " ", $ignored;'

native="${1:?native pub/static directory}"
ours="${2:?go-static-deploy pub/static directory}"
shift 2

list_packages() {
    (cd "$1" && find -L . -mindepth 4 -maxdepth 4 -type d | sed 's|^\./||')
}

all_packages=1
if [ $# -gt 0 ]; then
    packages=("$@")
    all_packages=0
else
    mapfile -t packages < <({ list_packages "$native"; list_packages "$ours"; } | sort -u)
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

total_diff=0
for package in "${packages[@]}"; do
    (cd "$native/$package" 2>/dev/null && find -L . -type f | sed 's|^\./||' | sort) > "$tmp/native.txt"
    (cd "$ours/$package" 2>/dev/null && find -L . -type f | sed 's|^\./||' | sort) > "$tmp/ours.txt"

    only_native=$(comm -23 "$tmp/native.txt" "$tmp/ours.txt" | grep -v '^js/bundle/')
    only_ours=$(comm -13 "$tmp/native.txt" "$tmp/ours.txt" | grep -v '^js/bundle/')
    different=()
    details=()
    while IFS= read -r file; do
        [ -z "$file" ] && continue
        a="$native/$package/$file"
        b="$ours/$package/$file"
        if [[ "$file" == js/bundle/* ]] || cmp -s "$a" "$b"; then
            continue
        fi
        if [ "$minified" = 1 ] && [[ "$file" == *.min.js || "$file" == *.min.css ]]; then
            continue
        fi
        detail=""
        if [[ "$file" == *.json ]] && detail=$(php -r "$php_compare_json" "$a" "$b" "$minified"); then
            continue
        fi
        different+=("$file")
        details+=("$detail")
    done < <(comm -12 "$tmp/native.txt" "$tmp/ours.txt")

    read -r hash_native count_native_bundled ignored_native < <(php -r "$php_bundle_contents" "$native/$package" "$minified")
    read -r hash_ours count_ours_bundled ignored_ours < <(php -r "$php_bundle_contents" "$ours/$package" "$minified")
    if [ "$hash_native" != "$hash_ours" ]; then
        different+=("js/bundle/* (merged contents: native $count_native_bundled entries, go $count_ours_bundled entries)")
        details+=("")
    fi

    count_native=$(wc -l < "$tmp/native.txt")
    count_ours=$(wc -l < "$tmp/ours.txt")
    echo "== $package: native $count_native files, go $count_ours files, $count_ours_bundled bundled, ${#different[@]} different"
    if [ "$ignored_native" != 0 ] || [ "$ignored_ours" != 0 ]; then
        echo "  ignored bundle entries with a .min version: native $ignored_native, go $ignored_ours"
    fi
    if [ -n "$only_native" ]; then
        echo "  only in native ($(echo "$only_native" | wc -l)):"
        echo "$only_native" | sed 's/^/    /' | head -20
    fi
    if [ -n "$only_ours" ]; then
        echo "  only in go-static-deploy ($(echo "$only_ours" | wc -l)):"
        echo "$only_ours" | sed 's/^/    /' | head -20
    fi
    for i in "${!different[@]}"; do
        echo "  differs: ${different[$i]}"
        [ -n "${details[$i]}" ] && echo "${details[$i]}"
    done
    if [ -n "$only_native" ] || [ -n "$only_ours" ] || [ ${#different[@]} -gt 0 ]; then
        total_diff=1
    fi
done

# Area level SRI hashes (newer Magento_Csp versions): pub/static/<area>/sri-hashes.json
for area in frontend adminhtml; do
    [ "$all_packages" = 1 ] || break
    a="$native/$area/sri-hashes.json"
    b="$ours/$area/sri-hashes.json"
    if [ ! -e "$a" ] && [ ! -e "$b" ]; then
        continue
    fi
    if [ ! -e "$b" ]; then
        echo "== $area/sri-hashes.json: only in native"
        total_diff=1
    elif [ ! -e "$a" ]; then
        echo "== $area/sri-hashes.json: only in go-static-deploy"
        total_diff=1
    elif ! detail=$(php -r "$php_compare_json" "$a" "$b" "$minified"); then
        echo "== $area/sri-hashes.json: differs"
        [ -n "$detail" ] && echo "$detail"
        total_diff=1
    else
        echo "== $area/sri-hashes.json: same"
    fi
done

if [ ${#packages[@]} -eq 0 ]; then
    echo "No packages found" >&2
    total_diff=1
fi

exit $total_diff
