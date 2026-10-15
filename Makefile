.PHONY: ci fmt-check vet test

ci: fmt-check vet test

fmt-check:
	@unformatted=$$(cd backend && gofmt -l .); \
	if [ -n "$$unformatted" ]; then echo "Run gofmt on:"; echo "$$unformatted"; exit 1; fi

vet:
	cd backend && go vet ./...

test:
	cd backend && go test -race ./...
