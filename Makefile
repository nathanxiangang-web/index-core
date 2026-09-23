GO ?= go
PG_CONTAINER ?= indexcore-pg
PG_PORT ?= 55432
TEST_DSN ?= postgres://indexcore:indexcore@localhost:$(PG_PORT)/indexcore?sslmode=disable
VERSION ?= 0.3.0-alpha
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
PKG ?= github.com/nathanxiangang-web/index-core/internal/runtime/version
LDFLAGS ?= -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT) -X $(PKG).Date=$(DATE)

.PHONY: pg-up pg-down build bin fmt test scale docker-build compose-up compose-down compose-smoke

pg-up:
	@docker rm -f $(PG_CONTAINER) >/dev/null 2>&1 || true
	@docker run -d --name $(PG_CONTAINER) \
		-e POSTGRES_PASSWORD=indexcore -e POSTGRES_USER=indexcore -e POSTGRES_DB=indexcore \
		-p $(PG_PORT):5432 postgres:18 >/dev/null
	@printf "waiting for postgres 18"; \
	for i in $$(seq 1 30); do \
		if docker exec $(PG_CONTAINER) pg_isready -U indexcore >/dev/null 2>&1; then echo " ready"; exit 0; fi; \
		printf "."; sleep 1; \
	done; echo " TIMEOUT"; exit 1

pg-down:
	@docker rm -f $(PG_CONTAINER) >/dev/null 2>&1 || true

build:
	$(GO) build ./...

# IndexCore Alpha binary with build identity.
bin:
	$(GO) build -ldflags "$(LDFLAGS)" -o bin/indexcore ./cmd/indexcore

fmt:
	$(GO) fmt ./...

# Tests run against a real PostgreSQL 18 per the accepted Store/runtime contract.
# -p 1 serializes packages so DB-backed packages do not reset the same schema concurrently.
test:
	INDEXCORE_TEST_DATABASE_URL="$(TEST_DSN)" $(GO) test -p 1 ./... $(ARGS)
# Accepted scale harness (>=20k resources). Override N with SCALE_N=<n>.
SCALE_N ?= 20000
scale:
	INDEXCORE_TEST_DATABASE_URL="$(TEST_DSN)" INDEXCORE_SCALE_TEST=1 INDEXCORE_SCALE_N=$(SCALE_N) \
		$(GO) test ./internal/runtime/scale -run TestScalePopulationAndDelta -v -timeout 20m

# Docker packaging.
docker-build:
	docker build -t indexcore:alpha \
		--build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg DATE=$(DATE) .

compose-up:
	docker compose up -d --build

compose-down:
	docker compose down
# Clean-volume Compose smoke (down -v -> up --build -> ready -> restart).
compose-smoke:
	bash scripts/compose-smoke.sh