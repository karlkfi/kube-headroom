//go:build tools

// Package tools pins the versions of the build tools this repo shells out to.
//
// This file is never compiled (the `tools` build tag excludes it from normal
// builds); the blank imports exist so `go mod tidy` keeps each tool's module in
// tools/go.mod and tools/go.sum. The Makefile builds each binary out of this
// module with `go build`, so tool versions are pinned here rather than as
// floating Makefile string vars. See docs/STATUS.md Q15.
package tools

import (
	_ "github.com/golangci/golangci-lint/v2/cmd/golangci-lint"
	_ "golang.org/x/vuln/cmd/govulncheck"
	_ "helm.sh/helm/v4/cmd/helm"
	_ "sigs.k8s.io/controller-tools/cmd/controller-gen"
	_ "sigs.k8s.io/kustomize/kustomize/v5"
)
