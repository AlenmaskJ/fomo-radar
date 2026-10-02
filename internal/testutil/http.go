package testutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Fixture loads a repository fixture relative to tests/fixtures.
func Fixture(t testing.TB, name string) []byte {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve fixture directory")
	}
	path := filepath.Join(filepath.Dir(file), "..", "..", "tests", "fixtures", name)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return body
}
