package core

import (
	"fmt"
	"testing"

	"github.com/thomdehoog/origoa/internal/ojson"
)

func benchBody(b *testing.B, js string) *ojson.Obj {
	b.Helper()
	o, err := ojson.Parse([]byte(js))
	if err != nil {
		b.Fatal(err)
	}
	return o
}

func benchFoundation(b *testing.B, seed int) *Foundation {
	b.Helper()
	f, err := Open(b.TempDir() + "/repo.git")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { f.Close() })
	for i := 0; i < seed; i++ {
		body := benchBody(b, fmt.Sprintf(`{"title":"seed %d","fields":{"n":"%d"}}`, i, i))
		if _, err := f.CreateArtifact(KindEntry, fmt.Sprintf("f%d", i%10), "part", body); err != nil {
			b.Fatal(err)
		}
	}
	return f
}

func BenchmarkCreateEntry(b *testing.B) {
	f := benchFoundation(b, 200)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := f.CreateArtifact(KindEntry, "bench", "part",
			benchBody(b, fmt.Sprintf(`{"title":"bench %d"}`, i))); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGetArtifact(b *testing.B) {
	f := benchFoundation(b, 200)
	m, err := f.CreateArtifact(KindEntry, "bench", "part", benchBody(b, `{"title":"target"}`))
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := f.Artifact(m.GUID); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSearch(b *testing.B) {
	f := benchFoundation(b, 200)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := f.Search("seed", "", "", 0); err != nil {
			b.Fatal(err)
		}
	}
}
