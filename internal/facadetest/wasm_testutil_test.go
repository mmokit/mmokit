package facadetest

import (
	"os"
	"testing"
)

// readFile reads the file at path or fails the test.
func readFile(t testing.TB, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
