//go:build wasip1

package main

// add is the smoke-test export: host calls add(2,3) and expects 5.
//
//go:wasmexport add
func add(a int32, b int32) int32 { return a + b }

// main must exist for the toolchain but is never run — the host
// instantiates this as a reactor and calls _initialize, not _start.
func main() {}
