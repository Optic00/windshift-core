//go:build tools

// Package main pins build- and test-only dependencies that no production
// import path references, so `go mod tidy` keeps them in go.mod.
//
// gldap and its testdirectory helper back the LDAP integration tests. Those
// tests live in the core-tests overlay repo, which has no go.mod of its own and
// builds against this module — so tidy run here cannot see the import and
// prunes the requirement, breaking the overlay build. This file is the
// tidy-visible anchor. The `tools` build tag is never set by a normal build or
// test run, so nothing is linked into the binary.
package main

import (
	_ "github.com/jimlambrt/gldap"
	_ "github.com/jimlambrt/gldap/testdirectory"
)
