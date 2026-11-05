set dotenv-load := false

# Default recipe: list available recipes
default:
    @just --list

# Prepare development dependencies
setup:
    go mod download

# Run CLI in human mode (default)
run *args:
    go run ./cmd/repo-person {{ args }}

# Run CLI in agent-facing mode
run-ai *args:
    AGENT=1 go run ./cmd/repo-person {{ args }}

# Run test suite
test:
    go test -race -cover ./...

# Build binary into bin/
build:
    mkdir -p bin
    go build -o bin/repo-person ./cmd/repo-person

# Install binary to GOPATH bin directory
install:
    go install ./cmd/repo-person

# Run static analysis and vet
lint:
    go vet ./...

# Alias for lint
vet: lint

# Format Go code
fmt:
    go fmt ./...

# Run static analysis and test suite in sequence
check:
    go mod tidy -diff
    go vet ./...
    just coverage

# Run race tests and enforce statement coverage with disposable per-run storage
coverage:
    bash scripts/check-coverage.sh

# Clean build and coverage outputs
clean:
    rm -rf bin coverage.out dist
