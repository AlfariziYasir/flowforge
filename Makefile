.PHONY: mocks test build vet fmt fmt-check install-mockery

# Install mockery if not present
install-mockery:
	go install github.com/vektra/mockery/v2@latest

# Generate mocks from .mockery.yaml
mocks:
	mockery

# Format code
fmt:
	gofmt -w ./cmd ./internal

# Check code formatting
fmt-check:
	@test -z "$$(gofmt -l ./cmd ./internal)" || (echo "gofmt reported unformatted files:" && gofmt -l ./cmd ./internal && exit 1)

# Run tests
test: fmt-check
	go test ./internal/... -race -count=1 -v

# Build
build:
	go build ./...

# Vet
vet:
	go vet ./...

# CI pipeline gate
ci: fmt-check vet build test

