package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"0.8.2", "0.8.1", true},
		{"0.10.0", "0.9.9", true},
		{"1.0", "0.99.99", true},
		{"0.8.1", "0.8.1", false},
		{"0.8.1", "0.8.2", false},
		{"0.8.1.1", "0.8.1", true},
		{"0.8.1", "dev", false},
		{"dev", "0.8.1", false},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestExtractAndMatch(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range map[string]string{"README.md": "read me", "./dv": "the binary"} {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body))})
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	archive := buf.Bytes()

	bin, err := extract(archive, ".tar.gz", "dv")
	if err != nil || string(bin) != "the binary" {
		t.Fatalf("extract = %q, %v", bin, err)
	}
	sum := sha256.Sum256(archive)
	sums := []byte(hex.EncodeToString(sum[:]) + "  dv_linux_amd64.tar.gz\n0000  dv_darwin_arm64.tar.gz\n")
	if !matches(sums, "dv_linux_amd64.tar.gz", archive) {
		t.Error("the archive's own checksum did not match")
	}
	if matches(sums, "dv_darwin_arm64.tar.gz", archive) || matches(sums, "dv_windows_amd64.zip", archive) {
		t.Error("a checksum matched another asset, or one not listed")
	}
}
