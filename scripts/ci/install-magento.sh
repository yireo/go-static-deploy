#!/usr/bin/env bash
#
# Create and install a Magento or Mage-OS project for the integration tests.
#
# Environment variables:
#   REPOSITORY_URL   Composer repository (e.g. https://mirror.mage-os.org/ or https://repo.mage-os.org/)
#   PROJECT_PACKAGE  Project package (e.g. magento/project-community-edition)
#   VERSION          Project version (e.g. 2.4.8-p5)
#   MAGENTO_DIR      Target directory (default: /var/www/magento)
#   DB_HOST, DB_NAME, DB_USER, DB_PASSWORD  Database connection (defaults: db, magento, root, root)
#
# The search engine modules are disabled, so no Elasticsearch/OpenSearch server is needed.
# The fixture theme Test/child (scripts/ci/fixtures/) is copied into app/design before the installation.

set -euo pipefail

: "${REPOSITORY_URL:?REPOSITORY_URL is required}"
: "${PROJECT_PACKAGE:?PROJECT_PACKAGE is required}"
: "${VERSION:?VERSION is required}"
MAGENTO_DIR="${MAGENTO_DIR:-/var/www/magento}"
DB_HOST="${DB_HOST:-db}"
DB_NAME="${DB_NAME:-magento}"
DB_USER="${DB_USER:-root}"
DB_PASSWORD="${DB_PASSWORD:-root}"
fixtures="$(cd "$(dirname "$0")" && pwd)/fixtures"

echo "Creating ${PROJECT_PACKAGE} ${VERSION} from ${REPOSITORY_URL} in ${MAGENTO_DIR}"
composer create-project --no-interaction --no-install --repository-url="${REPOSITORY_URL}" \
    "${PROJECT_PACKAGE}" "${MAGENTO_DIR}" "${VERSION}"

cd "${MAGENTO_DIR}"

# The project composer.json points to its own repository (repo.magento.com for Magento): use the given one instead.
REPOSITORY_URL="${REPOSITORY_URL}" php -r '
$json = json_decode(file_get_contents("composer.json"), true);
$json["repositories"] = [["type" => "composer", "url" => getenv("REPOSITORY_URL")]];
file_put_contents("composer.json", json_encode($json, JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES) . "\n");
'
# Older releases depend on packages with security advisories, which Composer blocks by default.
composer config audit.block-insecure false
composer install --no-interaction --no-progress

# Fixture theme (registered by setup:install), see scripts/ci/fixtures/
cp -a "${fixtures}/app/." app/

echo "Waiting for the database at ${DB_HOST}"
for i in $(seq 1 60); do
    if php -r 'new PDO("mysql:host=" . $argv[1], $argv[2], $argv[3]);' "${DB_HOST}" "${DB_USER}" "${DB_PASSWORD}" 2>/dev/null; then
        break
    fi
    if [ "$i" = 60 ]; then
        echo "Database is not available" >&2
        exit 1
    fi
    sleep 2
done

search_modules=$(grep -ohE "'[A-Za-z]+_[A-Za-z0-9]*(Elasticsearch|OpenSearch)[A-Za-z0-9]*'" vendor/*/*/registration.php \
    | tr -d "'" | sort -u | paste -sd, -)
echo "Disabling search modules: ${search_modules}"

php -d memory_limit=-1 bin/magento setup:install --no-interaction \
    --db-host="${DB_HOST}" --db-name="${DB_NAME}" --db-user="${DB_USER}" --db-password="${DB_PASSWORD}" \
    --base-url="http://localhost/" --backend-frontname=admin \
    --admin-firstname=Admin --admin-lastname=User --admin-email=admin@example.com \
    --admin-user=admin --admin-password=Admin123456 \
    --language=en_US --currency=USD --timezone=UTC --use-rewrites=1 \
    --disable-modules="${search_modules}"
