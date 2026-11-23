// Package foundationtest has helpers for testing code built on foundation: a log
// recorder and a recorder for the exit status.
//
// It is for tests only. [ExitRecorder] replaces the function that ends the
// process, so a binary that imported it and installed it would survive a failed
// run with exit status 0; the helpers take a [testing.TB] so they can only be
// installed from a test, and nothing outside a _test.go file should import this
// package.
package foundationtest
