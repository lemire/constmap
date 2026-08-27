package constmap

import (
	"math/rand"
	"testing"

	"github.com/cespare/xxhash/v2"
)

func TestHashKeysXXH64ShortMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(11))

	var keys []string
	var want []uint64
	for length := 0; length < 32; length++ {
		for trial := 0; trial < 8; trial++ {
			buf := make([]byte, length)
			for i := range buf {
				buf[i] = byte(rng.Intn(256))
			}
			s := string(buf)
			keys = append(keys, s)
			want = append(want, xxhash.Sum64String(s))
		}
	}
	// Also the exact benchmark key shape, which stays under 32 bytes for
	// all of batchMapN.
	for i := 0; i < 5000; i++ {
		s := fmtKey(i)
		keys = append(keys, s)
		want = append(want, xxhash.Sum64String(s))
	}

	// Exercise a range of batch sizes relative to the input, not just the
	// full batch at once, since the loop/indexing logic could have an
	// off-by-one that a single giant call wouldn't reveal.
	for _, n := range []int{0, 1, 2, 3, 31, 32, 33, 63, 64, 65, len(keys)} {
		if n > len(keys) {
			continue
		}
		out := make([]uint64, n)
		hashKeysXXH64Short(keys[:n], out)
		for i := 0; i < n; i++ {
			if out[i] != want[i] {
				t.Fatalf("n=%d i=%d: hashKeysXXH64Short(%q) = %#x, want %#x", n, i, keys[i], out[i], want[i])
			}
		}
	}
}

func TestMapManyLongKeyFallback(t *testing.T) {
	n := 2000
	keys := make([]string, n)
	values := make([]uint64, n)
	for i := 0; i < n; i++ {
		switch {
		case i == 0:
			keys[i] = "" // length-0 edge case alongside long keys
		case i%97 == 0:
			// >=32 bytes: forces hashManyShort to bail for the whole
			// chunk containing this key.
			keys[i] = fmtKey(i) + "-this-key-is-deliberately-long-enough-to-exceed-32-bytes"
		default:
			keys[i] = fmtKey(i)
		}
		values[i] = uint64(i * 31)
	}

	cm, err := New(keys, values)
	if err != nil {
		t.Fatal(err)
	}

	out := make([]uint64, n)
	cm.MapManyInto(out, keys)
	for i, k := range keys {
		want := cm.Map(k)
		if out[i] != want {
			t.Errorf("i=%d: MapMany(%q) = %d, want %d", i, k, out[i], want)
		}
		if out[i] != values[i] {
			t.Errorf("i=%d: MapMany(%q) = %d, want value %d", i, k, out[i], values[i])
		}
	}
}

func fmtKey(i int) string {
	// Avoid importing fmt in the hot test path; matches "key-%d".
	if i == 0 {
		return "key-0"
	}
	digits := "0123456789"
	var buf [16]byte
	pos := len(buf)
	n := i
	for n > 0 {
		pos--
		buf[pos] = digits[n%10]
		n /= 10
	}
	return "key-" + string(buf[pos:])
}
