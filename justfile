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
    go test -race -coverprofile=coverage.out ./...

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
    go test -race -coverprofile=coverage.out ./...
    bash scripts/check-coverage.sh

# Enforce statement coverage after tests
coverage:
    bash scripts/check-coverage.sh

# Clean up build binaries and temporary files
clean:
    rm -rf bin coverage.out .tmp dist
