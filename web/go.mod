// This is not a Go project. This file exists to keep the operator console OUT
// of the Go module's package graph.
//
// web/ contains no Go source of its own, but npm packages occasionally vendor
// Go files -- `flatted`, a transitive dependency of ESLint, ships one -- and the
// go command walks node_modules like any other directory. Without this,
// `go build ./...`, `go vet ./...` and `go test ./...` all pick up packages that
// are not ours, report them in their output, and would fail the build outright
// if one of them ever failed to compile.
//
// A nested go.mod makes the go command treat this subtree as a separate module
// and skip it entirely. Nothing ever builds or requires it.
module accesslink-console-not-a-go-module

go 1.21
