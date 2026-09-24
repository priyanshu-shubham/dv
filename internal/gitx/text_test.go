package gitx

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// endless is a file of the given byte as long as anyone reads, counting how
// much was read.
type endless struct {
	b    byte
	read int64
}

func (e *endless) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = e.b
	}
	e.read += int64(len(p))
	return len(p), nil
}

func TestReadTextStopsAtWhatItNeeds(t *testing.T) {
	const size = 1 << 30
	for _, c := range []struct {
		name string
		b    byte
		want error
	}{
		{"binary", 0, ErrBinary},
		{"too large", 'x', ErrTooLarge},
	} {
		src := &endless{b: c.b}
		if _, err := ReadText(src, size, 3<<20); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.want)
		}
		if src.read > sniffLen {
			t.Errorf("%s: read %d bytes of a %d-byte file to tell", c.name, src.read, int64(size))
		}
	}
}

func TestReadTextReadsText(t *testing.T) {
	text := strings.Repeat("line\n", 5000) // past the sniffed start
	b, err := ReadText(strings.NewReader(text), int64(len(text)), 3<<20)
	if err != nil || string(b) != text {
		t.Fatalf("got %d bytes, %v", len(b), err)
	}
	if b, err := ReadText(strings.NewReader(""), 0, 10); err != nil || len(b) != 0 {
		t.Fatalf("empty: %q, %v", b, err)
	}
	// A NUL past the start is text, as git has it.
	late := append(bytes.Repeat([]byte("a"), sniffLen), 0)
	if _, err := ReadText(bytes.NewReader(late), int64(len(late)), 1<<20); err != nil {
		t.Fatalf("a NUL after the start: %v", err)
	}
}

// A file that grew after its size was taken is held to the limit all the same.
func TestReadTextHoldsAGrownFileToTheLimit(t *testing.T) {
	src := io.LimitReader(&endless{b: 'x'}, 1<<20)
	if _, err := ReadText(src, 10, 1000); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("got %v", err)
	}
}

// Committed versions are read from git the same way: a binary or oversized
// blob is refused, a text one read whole.
func TestBlobsAreReadAsText(t *testing.T) {
	big := strings.Repeat("x", maxFileBytes+1)
	r := tempRepo(t, map[string]string{"bin.dat": "\x00\x01binary", "big.txt": big, "small.txt": "one\ntwo\n"})
	commitAll(t, r, "init")
	head, err := r.ResolveScope("working", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.FileAt("bin.dat", head, true); !errors.Is(err, ErrBinary) {
		t.Errorf("binary blob: %v", err)
	}
	if _, _, err := r.FileAt("big.txt", head, true); !errors.Is(err, ErrTooLarge) {
		t.Errorf("large blob: %v", err)
	}
	if lines, _, err := r.FileAt("small.txt", head, true); err != nil || len(lines) != 2 {
		t.Errorf("text blob: %q, %v", lines, err)
	}
	if _, _, err := r.FileAt("nope.txt", head, true); err == nil || errors.Is(err, ErrBinary) {
		t.Errorf("missing blob: %v", err)
	}

	write(t, r, "bin.dat", "now text\n")
	write(t, r, "big.txt", "short\n")
	for path, want := range map[string]string{"bin.dat": "binary", "big.txt": "too large"} {
		fd, err := r.Diff(head, FileEntry{Path: path, Status: "M"})
		switch {
		case err != nil:
			t.Errorf("%s: %v", path, err)
		case want == "binary" && !fd.Binary, want == "too large" && (!fd.TooLarge || fd.Binary):
			t.Errorf("%s: binary %v, too large %v, want %s", path, fd.Binary, fd.TooLarge, want)
		}
	}
}
