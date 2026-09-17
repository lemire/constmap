package constmap

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The files under testdata/interop were written by the other two
// implementations from the same input as interopData: rsconstmap (Rust) and
// fastconstmap (C/Python), plus the two formats fastconstmap 0.9 wrote with a
// different key hash. The three implementations are meant to read each
// other's files, so these tests are the guard on that promise: a change to
// the hash, the mixing, the seed sequence, the layout or the checksum shows up
// here as a failure to load or a wrong value.
//
// To regenerate: build a map from interopData in each implementation and
// save it (see the README of each repository).

func interopData() ([]string, []uint64) {
	keys := make([]string, 200)
	values := make([]uint64, 200)
	for i := range keys {
		keys[i] = fmt.Sprintf("key-%d", i)
		values[i] = 7 * uint64(i)
	}
	return keys, values
}

func interopPath(name string) string {
	return filepath.Join("testdata", "interop", name)
}

// checkInterop verifies every key of interopData against lookup, and that
// the absent keys report absent when the map can tell.
func checkInterop(t *testing.T, name string, lookup func(string) uint64, verified bool) {
	t.Helper()
	keys, values := interopData()
	for i, k := range keys {
		if got := lookup(k); got != values[i] {
			t.Fatalf("%s: Map(%q) = %d, want %d", name, k, got, values[i])
		}
	}
	if verified {
		for i := 0; i < 1000; i++ {
			k := fmt.Sprintf("absent-%d", i)
			if got := lookup(k); got != NotFound {
				t.Fatalf("%s: Map(%q) = %d, want NotFound", name, k, got)
			}
		}
	}
}

func TestInteropReadsOtherImplementations(t *testing.T) {
	for _, src := range []string{"rust", "fastconstmap"} {
		cm, err := LoadFromFile(interopPath(src + ".cmap"))
		if err != nil {
			t.Fatalf("%s.cmap: %v", src, err)
		}
		checkInterop(t, src+".cmap", cm.Map, false)

		vm, err := LoadVerifiedFromFile(interopPath(src + ".vmap"))
		if err != nil {
			t.Fatalf("%s.vmap: %v", src, err)
		}
		checkInterop(t, src+".vmap", vm.Map, true)

		pm, err := LoadPairedFromFile(interopPath(src + ".pmap"))
		if err != nil {
			t.Fatalf("%s.pmap: %v", src, err)
		}
		checkInterop(t, src+".pmap", pm.Map, true)
	}
}

// TestInteropWritesIdenticalBytes checks the stronger property that holds
// between this package and rsconstmap: for the same input they write the
// same bytes, since they share the hash, the seed sequence and the layout.
func TestInteropWritesIdenticalBytes(t *testing.T) {
	keys, values := interopData()
	cm, err := New(keys, values)
	if err != nil {
		t.Fatal(err)
	}
	vm, err := NewVerified(keys, values)
	if err != nil {
		t.Fatal(err)
	}
	pm := vm.Paired()

	for _, c := range []struct {
		name  string
		write func(*bytes.Buffer) error
	}{
		{"rust.cmap", func(b *bytes.Buffer) error { _, err := cm.WriteTo(b); return err }},
		{"rust.vmap", func(b *bytes.Buffer) error { _, err := vm.WriteTo(b); return err }},
		{"rust.pmap", func(b *bytes.Buffer) error { _, err := pm.WriteTo(b); return err }},
	} {
		want, err := os.ReadFile(interopPath(c.name))
		if err != nil {
			t.Fatal(err)
		}
		var got bytes.Buffer
		if err := c.write(&got); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got.Bytes(), want) {
			t.Errorf("%s: this package writes different bytes from rsconstmap (%d vs %d bytes)", c.name, got.Len(), len(want))
		}
	}
}

// TestInteropCountedHeaderIsIgnored checks that the key count fastconstmap
// stores (in CMAP0003's extra field, and in the padding word of the other
// two formats) makes no difference to the map read back.
func TestInteropCountedHeaderIsIgnored(t *testing.T) {
	fromRust, err := LoadFromFile(interopPath("rust.cmap"))
	if err != nil {
		t.Fatal(err)
	}
	fromC, err := LoadFromFile(interopPath("fastconstmap.cmap"))
	if err != nil {
		t.Fatal(err)
	}
	if fromRust.seed != fromC.seed || !bytes.Equal(wordsBytes(fromRust.data), wordsBytes(fromC.data)) {
		t.Error("CMAP0001 and CMAP0003 versions of the same map decoded differently")
	}
}

func wordsBytes(words []uint64) []byte {
	var b bytes.Buffer
	_ = writeWords(&b, words, chunkBuffer(len(words)))
	return b.Bytes()
}

// TestInteropLegacyFastconstmapIsNamed checks that a file from fastconstmap
// 0.9 or earlier, which used a different key hash, is refused with an error
// that says so rather than "invalid magic".
func TestInteropLegacyFastconstmapIsNamed(t *testing.T) {
	_, err := LoadFromFile(interopPath("fastconstmap-0.9.cmap"))
	if !errors.Is(err, errLegacyFastconstmap) {
		t.Errorf("fastconstmap-0.9.cmap: got %v, want errLegacyFastconstmap", err)
	}
	_, err = LoadVerifiedFromFile(interopPath("fastconstmap-0.9.vmap"))
	if !errors.Is(err, errLegacyFastconstmap) {
		t.Errorf("fastconstmap-0.9.vmap: got %v, want errLegacyFastconstmap", err)
	}
	// And the other readers name it too, rather than misreporting the type.
	var pm PairedVerifiedConstMap
	raw, _ := os.ReadFile(interopPath("fastconstmap-0.9.vmap"))
	if _, err := pm.ReadFrom(bytes.NewReader(raw)); !errors.Is(err, errLegacyFastconstmap) {
		t.Errorf("paired reader on fastconstmap-0.9.vmap: got %v", err)
	}
}
