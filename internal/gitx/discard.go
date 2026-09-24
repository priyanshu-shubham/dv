package gitx

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Discard puts files back as the last commit has them, dropping what is
// staged of them too: an edited or deleted file comes back, and one the last
// commit does not have - new, or a rename's new name - is deleted.
func (r *Repo) Discard(paths ...string) error {
	head := r.hasHead()
	for _, p := range paths {
		if !filepath.IsLocal(p) {
			return fmt.Errorf("%s is not in the repository", p)
		}
		if head {
			if _, err := r.run("cat-file", "-e", "HEAD:"+filepath.ToSlash(p)); err == nil {
				if _, err := r.run("checkout", "HEAD", "--", p); err != nil {
					return err
				}
				continue
			}
		}
		if _, err := r.run("rm", "--cached", "--quiet", "--ignore-unmatch", "--", p); err != nil {
			return err
		}
		if err := os.Remove(filepath.Join(r.Root, p)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
