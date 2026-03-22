.PHONY: build test test-race cover vet fmt check clean

build:
	go build -o bin/sprawler ./cmd/sprawler

test:
	go test ./... -count=1

test-race:
	go test -race ./... -count=1

COVER_PKGS := $(shell go list ./... | grep -v -E 'database/sqlc|logger|model|cmd/')

cover:
	go test -coverprofile=coverage.out -count=1 $(COVER_PKGS)
	go tool cover -func=coverage.out
	@rm -f coverage.out

vet:
	go vet ./...

fmt:
	gofmt -w .

check: vet fmt
	@test -z "$$(git diff --name-only)" || (echo "gofmt produced changes:" && git diff --name-only && exit 1)

clean:
	rm -rf bin/
