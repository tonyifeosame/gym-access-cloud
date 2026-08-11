# Entry points for the test suite.
#
# `make test` exists because a bare `go test ./...` is not trustworthy here. The
# suite is integration-first: it builds a database from migrations/ and talks to
# a real PostgreSQL. `go test` caches a passing package result and replays it
# whenever its key matches, and that key cannot describe whether PostgreSQL is
# reachable -- so a pass banked while the database was up is replayed unchanged
# after it goes away. -count=1 is the supported way to bypass that.

.PHONY: test test-fresh test-skip-db vet fmt build docker-render

# Build metadata. These must reach package-level vars named `version` and
# `commit` in package main -- `-X` against a symbol the linker cannot find is
# ignored silently, producing an unstamped binary with no warning. Verify with:
#   ./access-terminal-cloud-api & curl -s localhost:8080/health
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)

LDFLAGS = -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

# The binary for the systemd deployment. CGO off so it is static and does not
# depend on the build host's libc; -trimpath keeps build paths out of it.
build:
	CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="$(LDFLAGS)" \
		-o access-terminal-cloud-api .

# The image Render builds, built the way Render builds it: deploy/Dockerfile
# with the repository root as context and NO build arguments. Render passes
# none, so the binary is not stamped and /health reports "dev" for version and
# the commit from RENDER_GIT_COMMIT at runtime. Reproducing that here is the
# point -- `make build` and the deploy/README build both pass VERSION/COMMIT and
# so would not catch a Dockerfile that only works when they are supplied.
docker-render:
	docker build -f deploy/Dockerfile -t accesslink-api:render .

# The normal way to run the tests. Never served from cache.
test:
	go test -count=1 ./...

# Same, but also discards any result already banked -- use after a run that may
# have recorded a pass against a database that is no longer there.
test-fresh:
	go clean -testcache
	go test -count=1 ./...

# Deliberately skip the integration tests. Leaves the tenancy and sync SQL
# uncovered; the suite says so loudly when you do this.
test-skip-db:
	TEST_DB_SKIP=1 go test -count=1 ./...

vet:
	go vet ./...

fmt:
	gofmt -l .
