#!/usr/bin/env bash
#
# Sanity checks for the pub/static output of go-static-deploy.
#
# Usage: scripts/ci/check-static.sh <magento-root> <locale>...

set -uo pipefail

if [ $# -lt 2 ]; then
    echo "Usage: $0 <magento-root> <locale>..." >&2
    exit 2
fi

root="$1"
shift
static="${root}/pub/static"
errors=0

fail() {
    echo "FAIL: $*" >&2
    errors=$((errors + 1))
}

check_file() {
    if [ ! -s "$1" ]; then
        fail "$1 is missing or empty"
    fi
}

check_json() {
    check_file "$1"
    if [ -s "$1" ] && ! php -r 'json_decode(file_get_contents($argv[1]), false, 512, JSON_THROW_ON_ERROR);' "$1"; then
        fail "$1 is not valid JSON"
    fi
}

sri=0
if ls "${root}"/vendor/*/module-csp/Model/Deploy/Package/Processor/PostProcessor/Integrity.php >/dev/null 2>&1 \
    && php -r '$c = require $argv[1]; exit(empty($c["modules"]["Magento_Csp"]) ? 1 : 0);' "${root}/app/etc/config.php"; then
    sri=1
fi

check_file "${static}/deployed_version.txt"

for locale in "$@"; do
    for package in "frontend/Magento/luma/${locale}:css/styles-m.css" "adminhtml/Magento/backend/${locale}:css/styles.css"; do
        dir="${static}/${package%%:*}"
        check_file "${dir}/${package#*:}"
        check_file "${dir}/requirejs-config.js"
        check_json "${dir}/js-translation.json"
        if [ "$sri" = 1 ]; then
            check_json "${dir}/sri-hashes.json"
        fi
    done
done

if [ "$errors" -gt 0 ]; then
    echo "${errors} check(s) failed" >&2
    exit 1
fi
echo "All checks passed for locales: $*"
