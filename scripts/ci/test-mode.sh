#!/usr/bin/env bash
#
# Integration test for one deployment mode: deploy with bin/magento, deploy with go-static-deploy, validate the
# output (scripts/ci/validate-static.php) and compare it with the native output (scripts/compare.sh).
#
# Usage: scripts/ci/test-mode.sh <default|developer|production|production-minify|symlink>
#
# Environment variables:
#   MAGENTO_DIR   Magento root (default: /var/www/magento)
#   LOCALES       Locales to deploy (default: en_US nl_NL)
#   JOBS          Number of jobs of go-static-deploy (default: 4)
#   NATIVE_JOBS   Number of jobs of bin/magento (default: 1). With more jobs, the forked processes overwrite each
#                 other's <area>/sri-hashes.json, so entries are lost.
#   GSD_BIN       go-static-deploy binary (default: bin/go-static-deploy)
#   GSD_OPTIONS   Extra options for go-static-deploy (e.g. --less-compiler php)
#   DB_HOST, DB_NAME, DB_USER, DB_PASSWORD  Database connection (defaults: db, magento, root, root)
#
# The modes are meant to run in the order above: the production modes add a translation to the database.

set -uo pipefail

mode="${1:?mode}"
scripts="$(cd "$(dirname "$0")" && pwd)"
repo="$(cd "${scripts}/../.." && pwd)"
MAGENTO_DIR="${MAGENTO_DIR:-/var/www/magento}"
read -r -a locales <<< "${LOCALES:-en_US nl_NL}"
JOBS="${JOBS:-4}"
NATIVE_JOBS="${NATIVE_JOBS:-1}"
GSD_BIN="$(realpath "${GSD_BIN:-${repo}/bin/go-static-deploy}")"
read -r -a gsd_options <<< "${GSD_OPTIONS:-}"
export DB_HOST="${DB_HOST:-db}" DB_NAME="${DB_NAME:-magento}" DB_USER="${DB_USER:-root}" DB_PASSWORD="${DB_PASSWORD:-root}"
static="${MAGENTO_DIR}/pub/static"
failures=()

step() {
    echo
    echo "### $*"
}

fail() {
    echo "FAIL: $*" >&2
    failures+=("$*")
}

# Runs SQL on the Magento database.
sql() {
    php -r '
        $pdo = new PDO("mysql:host=" . getenv("DB_HOST") . ";dbname=" . getenv("DB_NAME"), getenv("DB_USER"), getenv("DB_PASSWORD"));
        $pdo->setAttribute(PDO::ATTR_ERRMODE, PDO::ERRMODE_EXCEPTION);
        $pdo->exec($argv[1]);' "$1"
}

set_mage_mode() {
    php -r '
        $file = $argv[1] . "/app/etc/env.php";
        $env = require $file;
        $env["MAGE_MODE"] = $argv[2];
        file_put_contents($file, "<?php\nreturn " . var_export($env, true) . ";\n");' "$MAGENTO_DIR" "$1"
}

set_minify() {
    sql "INSERT INTO core_config_data (scope, scope_id, path, value) VALUES
            ('default', 0, 'dev/js/minify_files', '$1'), ('default', 0, 'dev/css/minify_files', '$1')
         ON DUPLICATE KEY UPDATE value = VALUES(value)"
}

set_db_translation() {
    if [ "$1" = 1 ]; then
        sql "INSERT IGNORE INTO translation (string, store_id, translate, locale, crc_string)
             VALUES ('GSD database phrase', 0, 'GSD database vertaling', 'nl_NL', CRC32('GSD database phrase'))"
    else
        sql "DELETE FROM translation WHERE string = 'GSD database phrase'"
    fi
}

clean_static() {
    rm -rf "${static}/frontend" "${static}/adminhtml" "${static}/deployed_version.txt" \
        "${MAGENTO_DIR}/var/view_preprocessed" "${MAGENTO_DIR}/var/cache" "${MAGENTO_DIR}/var/page_cache"
}

# native_deploy <name>: deploy with bin/magento and move the output to /tmp/native/<name>
native_deploy() {
    step "bin/magento setup:static-content:deploy ($1)"
    clean_static
    local start=$SECONDS
    if ! (cd "$MAGENTO_DIR" && php bin/magento setup:static-content:deploy -f -j "$NATIVE_JOBS" "${locales[@]}"); then
        fail "bin/magento setup:static-content:deploy ($1)"
    fi
    echo "bin/magento: $((SECONDS - start))s"
    rm -rf "/tmp/native/$1"
    mkdir -p "/tmp/native/$1"
    mv "${static}/frontend" "${static}/adminhtml" "/tmp/native/$1/" 2>/dev/null
    clean_static
}

# gsd_deploy <description> <option>...
gsd_deploy() {
    local description="$1"
    shift
    step "go-static-deploy $description"
    clean_static
    local start=$SECONDS
    if ! "$GSD_BIN" -r "$MAGENTO_DIR" -f -j "$JOBS" -v "${gsd_options[@]}" "$@" "${locales[@]}"; then
        fail "go-static-deploy $description"
    fi
    echo "go-static-deploy: $((SECONDS - start))s"
}

# validate <description> <option>...
validate() {
    local description="$1"
    shift
    step "Validate $description"
    if ! php "${scripts}/validate-static.php" "$@" "$MAGENTO_DIR" "${locales[@]}"; then
        fail "validate $description"
    fi
}

# compare <description> <reference> [--minified]
compare() {
    step "Compare $1 with $2"
    if ! "${repo}/scripts/compare.sh" ${3:-} "$2" "$static"; then
        fail "compare $1 with $2"
    fi
}

# baseline <name> <option>...: deploy with go-static-deploy and keep a copy in /tmp/baseline/<name>
baseline() {
    local name="$1"
    shift
    gsd_deploy "(baseline $name)" "$@"
    rm -rf "/tmp/baseline/$name"
    mkdir -p "/tmp/baseline/$name"
    cp -a "${static}/frontend" "${static}/adminhtml" "/tmp/baseline/$name/"
}

"$GSD_BIN" --version

case "$mode" in
    default)
        set_mage_mode default
        set_minify 0
        native_deploy default
        gsd_deploy "(default mode)"
        validate "default mode" --mode=default --reference=/tmp/native/default
        compare "default mode" /tmp/native/default
        ;;
    developer)
        set_mage_mode developer
        set_minify 0
        native_deploy developer
        gsd_deploy "(developer mode)"
        validate "developer mode" --mode=developer --reference=/tmp/native/developer
        compare "developer mode" /tmp/native/developer
        ;;
    production)
        set_mage_mode production
        set_minify 0
        set_db_translation 1
        native_deploy production
        gsd_deploy "(production mode, with database)" --with-db
        validate "production mode" --mode=production --db-translation --reference=/tmp/native/production
        compare "production mode" /tmp/native/production
        ;;
    production-minify)
        set_mage_mode production
        set_minify 1
        set_db_translation 1
        native_deploy production-minify
        gsd_deploy "(production mode, minification, with database)" --with-db
        validate "production mode with minification" --mode=production --minify --db-translation \
            --reference=/tmp/native/production-minify
        compare "production mode with minification" /tmp/native/production-minify --minified
        set_minify 0
        set_db_translation 0
        ;;
    symlink)
        set_mage_mode production
        set_minify 0
        set_db_translation 0

        baseline sri
        gsd_deploy "(--symlink file)" --symlink file
        validate "--symlink file" --mode=production --symlink=file --reference=/tmp/baseline/sri
        compare "--symlink file" /tmp/baseline/sri

        baseline no-sri --no-sri
        for symlink in locale locale-contained; do
            gsd_deploy "(--symlink $symlink --no-sri)" --symlink "$symlink" --no-sri
            validate "--symlink $symlink" --mode=production --symlink="$symlink" --no-sri --reference=/tmp/baseline/no-sri
            compare "--symlink $symlink" /tmp/baseline/no-sri
        done
        ;;
    *)
        echo "Unknown mode: $mode" >&2
        exit 2
        ;;
esac

echo
if [ ${#failures[@]} -gt 0 ]; then
    echo "${#failures[@]} failure(s) in mode $mode:" >&2
    printf '  %s\n' "${failures[@]}" >&2
    exit 1
fi
echo "Mode $mode passed"
