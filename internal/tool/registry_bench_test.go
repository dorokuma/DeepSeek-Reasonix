package tool

import "testing"

func BenchmarkRegistryLookup(b *testing.B) {
	r := NewRegistry()
	// Register a handful of stubs if Builtins empty in this package; lookup still exercises map path.
	names := []string{"read_file", "write_file", "bash", "grep", "ls"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = r.Get(names[i%len(names)])
	}
}
