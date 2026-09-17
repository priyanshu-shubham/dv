// dv opens a local diff reviewer for the git repository you run it in, or a
// file browser for a folder outside git. It serves a browser UI on loopback,
// and any comments you leave are written to .dv/comments.json inside that
// repository — never committed, because dv adds the directory to
// .git/info/exclude on first run.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"hash/fnv"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"dv/internal/gitx"
	"dv/internal/permit"
	"dv/internal/server"
	"dv/internal/store"
	"dv/internal/symindex"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "dv:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		port        = flag.Int("port", 0, "port to listen on (0 picks one from the repository's path)")
		host        = flag.String("host", "127.0.0.1", "address to bind")
		dir         = flag.String("C", ".", "repository directory")
		noOpen      = flag.Bool("no-open", false, "do not open a browser")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: dv [flags]\n       dv reset [-y]\n\n"+
			"Review the current repository's diff in a browser (outside git, read the\n"+
			"folder's files). reset deletes the\n"+
			"review's comments and viewed marks, to start it over.\n\nflags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *showVersion {
		fmt.Println("dv " + version)
		return nil
	}
	switch cmd := flag.Arg(0); cmd {
	case "":
	case "reset":
		return reset(*dir, flag.Args()[1:])
	case "claude":
		return claude(flag.Args()[1:])
	default:
		return fmt.Errorf("unknown command %q (dv -h lists them)", cmd)
	}

	repo, err := gitx.Open(*dir)
	if err != nil {
		return err
	}
	st, err := store.Open(repo.Root, repo.Name())
	if err != nil {
		return err
	}
	vw, err := store.OpenViewed(repo.Root)
	if err != nil {
		return err
	}
	sessions, err := store.OpenSessions(repo.Root)
	if err != nil {
		return err
	}
	if repo.IsGit() {
		excludeNotes(repo)
	}
	healHooks()

	ix := symindex.New(repo.Root, repo)
	ix.BuildAsync()

	if !server.AssetsBuilt() {
		return fmt.Errorf("this binary has no UI bundle in it - run `make build` (needs Node) and try again")
	}

	srv := server.New(repo, st, vw, sessions, ix)
	defer srv.Close()
	ln, err := listen(*host, *port, repo.Root)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("http://%s", ln.Addr().String())

	doing := "reviewing"
	if !repo.IsGit() {
		doing = "reading (not a git repository, so no diff)"
	}
	fmt.Printf("\n  \033[1m%s\033[0m — %s %s\n", repo.Name(), doing, repo.Root)
	fmt.Printf("  \033[1;32m%s\033[0m\n", url)
	fmt.Printf("  comments → %s\n\n  ctrl-c to stop\n\n", st.Path())

	if !*noOpen {
		openBrowser(url)
	}

	if unannounce, err := store.Announce(repo.Root, url); err != nil {
		fmt.Fprintln(os.Stderr, "dv: warning: Claude Code's prompts cannot find this dv:", err)
	} else {
		defer unannounce()
	}

	httpSrv := &http.Server{
		Handler: srv.Handler(),
		// No write timeout: a session's events stream for as long as the page is
		// open, and a permission prompt waits on the reader. Read timeouts still
		// bound a stuck client.
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       60 * time.Second,
	}
	// Stopping has to return through the deferred unannounce, or the next hook
	// goes looking for a dv that is not there.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		httpSrv.Close()
	}()
	if err := httpSrv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// version is stamped in by release builds (see .goreleaser.yaml).
var version = "dev"

func excludeNotes(repo *gitx.Repo) {
	if added, err := store.EnsureExcluded(repo.GitDir); err != nil {
		fmt.Fprintln(os.Stderr, "dv: warning: could not update .git/info/exclude:", err)
	} else if added {
		fmt.Println("dv: added /.dv/ to .git/info/exclude so review notes stay out of git")
	}
}

// reset deletes the review's comments and viewed marks. A dv already running
// on the repository picks it up without a restart, and so do its open pages.
func reset(dir string, args []string) error {
	fl := flag.NewFlagSet("dv reset", flag.ExitOnError)
	yes := fl.Bool("y", false, "delete without asking")
	fl.StringVar(&dir, "C", dir, "repository directory")
	fl.Parse(args)

	repo, err := gitx.Open(dir)
	if err != nil {
		return err
	}
	st, err := store.Open(repo.Root, repo.Name())
	if err != nil {
		return err
	}
	vw, err := store.OpenViewed(repo.Root)
	if err != nil {
		return err
	}
	threads, marks := len(st.Threads()), vw.Count()
	if threads+marks == 0 {
		fmt.Println("dv: nothing to reset in " + repo.Name())
		return nil
	}
	if !*yes {
		what := describe(threads, marks)
		if fi, err := os.Stdin.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
			return fmt.Errorf("reset would delete %s; pass -y to go ahead", what)
		}
		fmt.Printf("Delete %s in %s? [y/N] ", what, repo.Name())
		answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			fmt.Println("dv: nothing deleted")
			return nil
		}
	}
	// Counted again: a running dv may have added to either while we asked.
	if threads, err = st.Reset(); err != nil {
		return err
	}
	if marks, err = vw.Reset(); err != nil {
		return err
	}
	fmt.Println("dv: deleted " + describe(threads, marks))
	return nil
}

// claude is what Claude Code's hooks run. The page puts them in its settings
// and takes them out.
func claude(args []string) error {
	if strings.Join(args, " ") != "hook" {
		return fmt.Errorf("dv claude hook is for Claude Code to run; dv's hooks are added from its page")
	}
	permit.RunHook(os.Stdin, os.Stdout)
	return nil
}

// healHooks keeps Claude Code's hooks working after dv moves: they are pointed
// at this dv once the one they ran is gone.
func healHooks() {
	path, err := permit.SettingsPath()
	if err != nil {
		return
	}
	exe, err := permit.Exe()
	if err != nil {
		return
	}
	switch old, err := permit.Heal(path, exe); {
	case old == "":
	case err != nil:
		fmt.Fprintf(os.Stderr, "dv: warning: Claude Code's hooks run %s, which is gone, and could not be pointed here: %v\n", old, err)
	default:
		fmt.Printf("dv: Claude Code's hooks ran %s, which is gone; they run %s now\n", old, exe)
	}
}

func describe(threads, marks int) string {
	switch {
	case marks == 0:
		return count(threads, "comment thread")
	case threads == 0:
		return count(marks, "viewed mark")
	}
	return count(threads, "comment thread") + " and " + count(marks, "viewed mark")
}

func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// Each repository gets its own port in [basePort, basePort+portSpan), hashed
// from its root. The URL is the browser's storage origin, so a shared port would
// carry one repository's remembered scope, filters and viewed files into
// another, and leave an old tab silently showing whichever repository took the
// port next. A stable one also keeps bookmarks and open tabs working across runs.
const (
	basePort = 41100
	portSpan = 900
)

// portTries bounds the search past a busy home port before giving up.
const portTries = 200

func homePort(root string) int {
	h := fnv.New32a()
	h.Write([]byte(root))
	return basePort + int(h.Sum32()%portSpan)
}

func listen(host string, port int, root string) (net.Listener, error) {
	if port != 0 {
		return net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	}
	home := homePort(root)
	var firstErr error
	for i := range portTries {
		p := basePort + (home-basePort+i)%portSpan
		ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(p)))
		if err == nil {
			return ln, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, fmt.Errorf("no free port in %d tries from %d: %w", portTries, home, firstErr)
}

// openBrowser makes a best effort and stays silent on failure — the URL is
// already printed, which is the fallback.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		// WSL included: wslview understands Windows' default browser, and
		// xdg-open covers ordinary Linux desktops.
		if path, err := exec.LookPath("wslview"); err == nil {
			cmd = exec.Command(path, url)
		} else {
			cmd = exec.Command("xdg-open", url)
		}
	}
	if cmd != nil {
		cmd.Stdout, cmd.Stderr = nil, nil
		_ = cmd.Start()
	}
}
