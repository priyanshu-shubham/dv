package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// jsonFile is one document under .dv that a process keeps in memory while
// others may write it too: `dv reset`, an agent, an editor. Reading it again
// whenever it has moved is what stops the next save here from putting back
// whatever they changed.
type jsonFile struct {
	path  string
	stamp string // the version last read or written here; "" is no file
}

// load decodes the file into v if it moved since the last load or save, and
// reports whether it did. A missing file leaves v as the caller made it.
func (f *jsonFile) load(v any) (bool, error) {
	stamp := stampOf(f.path)
	if stamp == f.stamp {
		return false, nil
	}
	if stamp != "" {
		b, err := os.ReadFile(f.path)
		if err != nil && !os.IsNotExist(err) {
			return false, err
		}
		if err == nil {
			if err := json.Unmarshal(b, v); err != nil {
				return false, fmt.Errorf("%s is not valid JSON: %w", f.path, err)
			}
		}
	}
	f.stamp = stamp
	return true, nil
}

// save writes to a temp file and renames it into place, so a crash mid-write
// cannot leave half a file.
func (f *jsonFile) save(v any) error {
	if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, f.path); err != nil {
		return err
	}
	f.stamp = stampOf(f.path)
	return nil
}

func (f *jsonFile) remove() error {
	if err := os.Remove(f.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	f.stamp = ""
	return nil
}

// stampOf identifies one version of a file; "" is no file.
func stampOf(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d.%d", fi.Size(), fi.ModTime().UnixNano())
}
