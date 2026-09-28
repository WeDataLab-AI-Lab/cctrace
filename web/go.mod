// Not a Go component. This file exists so `go list ./...` from the repository
// root stops at web/ instead of walking into node_modules, where npm packages
// occasionally ship Go source of their own (flatted/golang, today).
//
// Without it those packages join every `go build`, `go vet`, `go test ./...` and
// lint run. Harmless while they compile; a compile error or lint finding in a
// dependency we never wrote would break our build with no file of ours involved.
//
// web/ has no Go source of its own, so nothing is excluded that we care about.
module cctrace-web-not-a-go-module

go 1.25.0
