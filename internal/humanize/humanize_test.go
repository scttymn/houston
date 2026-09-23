package humanize

import "testing"

func TestBytes(t *testing.T) {
	for n, want := range map[int64]string{5: "5 B", 1023: "1023 B", 1536: "1.5 KB", 404000000: "385 MB", 410000000: "391 MB", 1610612736: "1.5 GB", 1 << 50: "1.02e+03 TB"} {
		if got := Bytes(n); got != want {
			t.Errorf("Bytes(%d) = %q, want %q", n, got, want)
		}
	}
}
