package permit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// theirs is a settings file with a life of its own: keys out of alphabetical
// order, a hook of the user's on an event dv also uses, and a shell && that
// json.Marshal would escape.
const theirs = `{
  "model": "opus",
  "hooks": {
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "printf '\\a' && exit 0"
          }
        ]
      }
    ],
    "PermissionRequest": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "bell && exit 0"
          }
        ]
      }
    ]
  },
  "effortLevel": "high"
}
`

func hooksIn(t *testing.T, path string) map[string][]group {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Hooks map[string][]group `json:"hooks"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("%v in:\n%s", err, b)
	}
	return s.Hooks
}

func TestInstallKeepsWhatWasThere(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(path, []byte(theirs), 0o600)

	changed, err := Install(path, "/opt/bin/dv")
	if err != nil || !changed {
		t.Fatalf("install: %v, changed %v", err, changed)
	}
	b, _ := os.ReadFile(path)
	text := string(b)
	if strings.Contains(text, `\u0026`) {
		t.Fatalf("the user's && came back escaped:\n%s", text)
	}
	order := []string{`"model"`, `"hooks"`, `"Stop"`, `"PermissionRequest"`, `"PostToolUse"`, `"effortLevel"`}
	for i := 1; i < len(order); i++ {
		if strings.Index(text, order[i-1]) > strings.Index(text, order[i]) {
			t.Fatalf("%s moved after %s:\n%s", order[i-1], order[i], text)
		}
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode became %v", fi.Mode().Perm())
	}

	h := hooksIn(t, path)
	pr := h["PermissionRequest"]
	if len(pr) != 2 || pr[0].Hooks[0].Command != "bell && exit 0" {
		t.Fatalf("PermissionRequest: %+v", pr)
	}
	if mine := pr[1].Hooks[0]; mine.Command != "/opt/bin/dv" || strings.Join(mine.Args, " ") != "claude hook" || mine.Timeout != 86400 {
		t.Fatalf("dv's hook: %+v", mine)
	}
	if len(h["PostToolUse"]) != 1 || len(h["PostToolUseFailure"]) != 1 || len(h["Stop"]) != 1 {
		t.Fatalf("hooks: %+v", h)
	}
}

func TestInstallAgainChangesNothingAndMovingReplaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if changed, err := Install(path, "/opt/bin/dv"); err != nil || !changed {
		t.Fatalf("into no file: %v, changed %v", err, changed)
	}
	if changed, err := Install(path, "/opt/bin/dv"); err != nil || changed {
		t.Fatalf("again: %v, changed %v", err, changed)
	}
	if _, err := Install(path, "/home/me/bin/dv"); err != nil {
		t.Fatal(err)
	}
	for event, groups := range hooksIn(t, path) {
		if len(groups) != 1 || groups[0].Hooks[0].Command != "/home/me/bin/dv" {
			t.Fatalf("%s after the binary moved: %+v", event, groups)
		}
	}
}

func TestInstallWritesThroughALink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "dotfiles-settings.json")
	os.WriteFile(real, []byte(theirs), 0o644)
	link := filepath.Join(dir, "settings.json")
	if err := os.Symlink(real, link); err != nil {
		t.Skip(err)
	}
	if _, err := Install(link, "/opt/bin/dv"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the link was replaced by a file")
	}
	if len(hooksIn(t, real)["PostToolUse"]) != 1 {
		t.Fatal("the linked file did not get the hooks")
	}
}

func TestInstallRefusesWhatItCannotRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(path, []byte(`{"hooks": [1, 2]}`), 0o644)
	if _, err := Install(path, "/opt/bin/dv"); err == nil {
		t.Fatal("installed into hooks that are not an object")
	}
	if b, _ := os.ReadFile(path); string(b) != `{"hooks": [1, 2]}` {
		t.Fatalf("the file was touched: %s", b)
	}
}

func TestUninstallLeavesTheFileAsItWas(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(path, []byte(theirs), 0o600)
	// A space in the path would split it into a command that is not dv.
	if _, err := Install(path, "/Applications/My Tools/dv"); err != nil {
		t.Fatal(err)
	}
	if exe, err := Installed(path); err != nil || exe != "/Applications/My Tools/dv" {
		t.Fatalf("installed: %q, %v", exe, err)
	}
	if changed, err := Install(path, "/Applications/My Tools/dv"); err != nil || changed {
		t.Fatalf("installing again: %v, changed %v", err, changed)
	}

	if changed, err := Uninstall(path); err != nil || !changed {
		t.Fatalf("uninstall: %v, changed %v", err, changed)
	}
	if b, _ := os.ReadFile(path); string(b) != theirs {
		t.Fatalf("uninstall left:\n%s", b)
	}
	if exe, err := Installed(path); err != nil || exe != "" {
		t.Fatalf("installed after uninstall: %q, %v", exe, err)
	}
	if changed, err := Uninstall(path); err != nil || changed {
		t.Fatalf("again: %v, changed %v", err, changed)
	}
}

func TestUninstallDropsTheHooksItEmptied(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	os.WriteFile(path, []byte(`{"model": "opus"}`), 0o644)
	Install(path, "/opt/bin/dv")
	if _, err := Uninstall(path); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "{\n  \"model\": \"opus\"\n}\n" {
		t.Fatalf("uninstall left:\n%s", b)
	}

	none := filepath.Join(dir, "none.json")
	if changed, err := Uninstall(none); err != nil || changed {
		t.Fatalf("with no file: %v, changed %v", err, changed)
	}
	if _, err := os.Stat(none); err == nil {
		t.Fatal("uninstall made a settings file")
	}
}

func TestHealOnlyReplacesADvThatIsGone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	kept := filepath.Join(dir, "kept", "dv")
	os.MkdirAll(filepath.Dir(kept), 0o755)
	os.WriteFile(kept, nil, 0o755)
	here := filepath.Join(dir, "here", "dv")

	if old, err := Heal(path, here); err != nil || old != "" {
		t.Fatalf("with no hooks: %q, %v", old, err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("heal installed hooks nobody asked for")
	}

	Install(path, kept)
	if old, err := Heal(path, here); err != nil || old != "" {
		t.Fatalf("with their dv there: %q, %v", old, err)
	}

	os.Remove(kept)
	if old, err := Heal(path, here); err != nil || old != kept {
		t.Fatalf("with their dv gone: %q, %v", old, err)
	}
	if exe, _ := Installed(path); exe != here {
		t.Fatalf("hooks run %q", exe)
	}
}
