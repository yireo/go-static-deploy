#!/usr/bin/env bash
#
# Compare two pub/static trees, for instance the output of bin/magento setup:static-content:deploy
# (copied to another directory) with the output of go-static-deploy.
#
# Usage: scripts/compare.sh <native-pub-static> <go-pub-static> [package-path...]
#   package-path: e.g. frontend/Magento/luma/en_US (default: all packages found in <native-pub-static>)
#
# JSON files (js-translation.json, sri-hashes.json) are compared semantically when php is available.
# JS bundles (js/bundle/*.js) are compared by their merged contents, because Magento fills them in
# (unsorted) filesystem order. For the same reason the hashes of bundles in sri-hashes.json are ignored.

set -u

php_compare_json='
$a = json_decode(file_get_contents($argv[1]), true);
$b = json_decode(file_get_contents($argv[2]), true);
if (str_ends_with($argv[1], "sri-hashes.json")) {
    $filter = fn($v, $k) => !str_contains($k, "/js/bundle/");
    $a = array_filter($a, $filter, ARRAY_FILTER_USE_BOTH);
    $b = array_filter($b, $filter, ARRAY_FILTER_USE_BOTH);
}
exit($a == $b ? 0 : 1);'

php_bundle_contents='
$result = [];
$prefix = "require.config({\"config\": {\n        \"";
foreach (glob($argv[1] . "/js/bundle/*.js") as $file) {
    $content = file_get_contents($file);
    if (!str_starts_with($content, $prefix)) {
        continue;
    }
    $content = substr($content, strlen($prefix));
    $pool = substr($content, 0, strpos($content, "\""));
    $json = substr($content, strlen($pool) + 2, strpos($content, "\n}});\n") - strlen($pool) - 2);
    $result[$pool] = array_merge($result[$pool] ?? [], json_decode($json, true) ?? []);
}
foreach ($result as &$pool) {
    ksort($pool);
}
echo md5(serialize($result)), " ", array_sum(array_map("count", $result));'

native="${1:?native pub/static directory}"
ours="${2:?go-static-deploy pub/static directory}"
shift 2

if [ $# -gt 0 ]; then
    packages=("$@")
else
    mapfile -t packages < <(cd "$native" && find . -mindepth 4 -maxdepth 4 -type d | sed 's|^\./||' | sort)
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
    while IFS= read -r file; do
        [ -z "$file" ] && continue
        a="$native/$package/$file"
        b="$ours/$package/$file"
        if [[ "$file" == js/bundle/* ]] || cmp -s "$a" "$b"; then
            continue
        fi
        if [[ "$file" == *.json ]] && php -r "$php_compare_json" "$a" "$b"; then
            continue
        fi
        different+=("$file")
    done < <(comm -12 "$tmp/native.txt" "$tmp/ours.txt")

    bundles_native=$(php -r "$php_bundle_contents" "$native/$package")
    bundles_ours=$(php -r "$php_bundle_contents" "$ours/$package")
    if [ "$bundles_native" != "$bundles_ours" ]; then
        different+=("js/bundle/* (merged contents: native $bundles_native, go $bundles_ours)")
    fi

    count_native=$(wc -l < "$tmp/native.txt")
    count_ours=$(wc -l < "$tmp/ours.txt")
    echo "== $package: native $count_native files, go $count_ours files, ${#different[@]} different"
    if [ -n "$only_native" ]; then
        echo "  only in native:"
        echo "$only_native" | sed 's/^/    /' | head -20
    fi
    if [ -n "$only_ours" ]; then
        echo "  only in go-static-deploy:"
        echo "$only_ours" | sed 's/^/    /' | head -20
    fi
    for file in "${different[@]}"; do
        echo "  differs: $file"
    done
    if [ -n "$only_native" ] || [ -n "$only_ours" ] || [ ${#different[@]} -gt 0 ]; then
        total_diff=1
    fi
done

exit $total_diff
