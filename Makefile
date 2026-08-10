# Entry points for the test suite.
#
# `make test` exists because a bare `go test ./...` is not trustworthy here. The
# suite is integration-first: it builds a database from migrations/ and talks to
# a real PostgreSQL. `go test` caches a passing package result and replays it
# whenever its key matches, and that key cannot describe whether PostgreSQL is
# reachable -- so a pass banked while the database was up is replayed unchanged
# after it goes away. -count=1 is the supported way to bypass that.

.PHONY: test test-fresh test-skip-db vet fmt

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
