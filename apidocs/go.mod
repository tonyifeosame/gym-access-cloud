// apidocs is a Node project (the documentation site), not part of the Go
// module. This file exists so `go build ./...`, `go vet ./...` and
// `go test ./...` at the repository root stop at this directory instead of
// descending into node_modules, where npm packages occasionally carry Go
// sources of their own. There is no Go code here.
module accesslink-apidocs-not-go

go 1.24
