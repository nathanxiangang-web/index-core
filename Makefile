GO ?= go
PG_CONTAINER ?= indexcore-pg
PG_PORT ?= 55432
TEST_DSN ?= postgres://indexcore:indexcore@localhost:$(PG_PORT)/indexcore?sslmode=disable

.PHONY: pg-up pg-down build test fmt

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

fmt:
	$(GO) fmt ./...

# Gate 2 tests run against a real PostgreSQL 18 (Issue #44 requirement).
test:
	INDEXCORE_TEST_DATABASE_URL="$(TEST_DSN)" $(GO) test ./... $(ARGS)