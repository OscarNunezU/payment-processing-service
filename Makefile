.PHONY: ci fmt-check vet test test-integration vulncheck

TEST_DATABASE_URL = postgres://payments:payments@localhost:5432/payments_test?sslmode=disable

# Pinned, so a new release of the tool never changes what the CI checks.
GOVULNCHECK = golang.org/x/vuln/cmd/govulncheck@v1.9.0

ci: fmt-check vet test

fmt-check:
	@unformatted=$$(cd backend && gofmt -l .); \
	if [ -n "$$unformatted" ]; then echo "Run gofmt on:"; echo "$$unformatted"; exit 1; fi

vet:
	cd backend && go vet ./...

test:
	cd backend && go test -race ./...

vulncheck:
	cd backend && go run $(GOVULNCHECK) ./...

test-integration:
	docker compose up -d --wait postgres
	docker compose exec -T postgres sh -c 'createdb -U payments payments_test 2>/dev/null || true'
	cd backend && DATABASE_URL='$(TEST_DATABASE_URL)' go test -race -count=1 ./...
