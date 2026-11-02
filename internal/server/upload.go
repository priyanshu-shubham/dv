package server

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxUpload = 256 << 20

// stallLimit is how long a body may go without a byte arriving.
const stallLimit = time.Minute

// steady lets a large body take as long as it keeps arriving. The server's
// read timeout is for the whole request, which a picture sent from a phone on
// a poor signal can outlast.
func steady(w http.ResponseWriter, r *http.Request) io.ReadCloser {
	return &steadyBody{ReadCloser: r.Body, rc: http.NewResponseController(w)}
}

type steadyBody struct {
	io.ReadCloser
	rc  *http.ResponseController
	err error // once the body has ended
}

// Read leaves the deadline be once the body has ended: the server then reads
// on the connection for the next request, which this deadline would cut.
func (b *steadyBody) Read(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	b.rc.SetReadDeadline(time.Now().Add(stallLimit))
	n, err := b.ReadCloser.Read(p)
	b.err = err
	return n, err
}

// handleUpload saves a file added to a message; the page attaches it by the
// path that comes back.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	given := r.URL.Query().Get("name")
	name, err := s.save(given, http.MaxBytesReader(w, steady(w, r), maxUpload))
	if errors.As(err, new(*http.MaxBytesError)) {
		err = fmt.Errorf("%s is over the %d MB dv takes", given, maxUpload>>20)
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": name})
}

// save writes a file sent to a session into the folder's root, and returns
// the name it took there.
func (s *Server) save(given string, body io.Reader) (string, error) {
	name := uploadName(given)
	if name == "" {
		return "", fmt.Errorf("the file needs a name")
	}
	f, name, err := createFree(s.repo.Root, name)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(f, body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(filepath.Join(s.repo.Root, name))
		return "", err
	}
	return name, nil
}

// uploadName keeps only the last part of what a browser names a file, which
// on Windows may still carry its folders.
func uploadName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	if name == "." || name == ".." || name == "/" {
		return ""
	}
	return name
}

// createFree makes name in dir, or name-2, name-3... beside what is there:
// a file added never overwrites one.
func createFree(dir, name string) (*os.File, string, error) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if stem == "" {
		stem, ext = name, ""
	}
	for n := 1; n < 1000; n++ {
		try := name
		if n > 1 {
			try = fmt.Sprintf("%s-%d%s", stem, n, ext)
		}
		f, err := os.OpenFile(filepath.Join(dir, try), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			return f, try, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("too many files are named like %s", name)
}
