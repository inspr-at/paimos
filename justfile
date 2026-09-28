# PAIMOS AEON development recipes

db_url := "postgres://aeon:aeon@127.0.0.1:55432/aeon?sslmode=disable"

# Start a local Postgres 18 + pgvector (Docker via Colima)
db-up:
    docker run -d --name aeon-dev-db -p 55432:5432 -e POSTGRES_USER=aeon -e POSTGRES_PASSWORD=aeon -e POSTGRES_DB=aeon pgvector/pgvector:pg18 >/dev/null 2>&1 || docker start aeon-dev-db >/dev/null
    until docker exec aeon-dev-db pg_isready -U aeon >/dev/null 2>&1; do sleep 1; done
    @echo "postgres ready at {{db_url}}"

# Stop the local Postgres
db-down:
    docker stop aeon-dev-db

# Go tests. The URL is a maintenance database; each test creates its own.
test:
    AEON_TEST_DATABASE_URL="postgres://aeon:aeon@127.0.0.1:55432/aeon?sslmode=disable" go test ./...

# Web typecheck and build
web-check:
    cd web && npm run typecheck && npm run build

# Playwright smoke against a running server (BASE_URL defaults to http://127.0.0.1:8080)
e2e:
    cd web && npx playwright install chromium && npm run e2e

# Build the web app and the binary
build: web-check
    go build -o bin/paimos ./cmd/aeon

# Run the server locally
dev:
    AEON_DATABASE_URL="{{db_url}}" go run ./cmd/aeon serve

# Release checks (bundle pin and version source)
release-check:
    node scripts/verify-release.mjs

# Prepared rules wrapper drift; does not activate instructions or receive rules.
rules-check:
    go run ./scripts/rules-bootstrap check

# Release history manifest (inspr.release-history.v1) embedded in the server; reads the local tags.
release-history:
    go run ./internal/releasehistory/generate -repo . -repository inspr-at/aeon -offline

# Pack the neutral INSPR quote document profile (AEON-155) into the reproducible
# tar that `aeon quote-profile apply --bundle -` reads; prints its SHA-256.
quote-profile-inspr out="dist/quote-profile-inspr.tar":
    go run ./internal/business/quotes/profiletar -src internal/business/quotes/profiles/inspr -out {{out}}

# Apply the INSPR profile to a test tenant and print its sample quotes as PDFs and
# page PNGs (needs `just db-up`, `just web-check` and Playwright's Chromium).
quote-profile-inspr-samples out="tmp/quote-profile-inspr":
    mkdir -p {{out}}
    AEON_TEST_DATABASE_URL="{{db_url}}" AEON_PROFILE_SAMPLE_DIR="$(cd {{out}} && pwd)" go test -count=1 -run TestINSPRProfileAppliesAndRendersSampleQuote ./internal/business/quotes/
    for f in {{out}}/*.pdf; do pdftoppm -r 110 -png "$f" "${f%.pdf}"; done
