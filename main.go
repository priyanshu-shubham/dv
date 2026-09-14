// dv opens a local diff reviewer for the git repository you run it in. It
// serves a browser UI on loopback, and any comments you leave are written to
// .dv/comments.json inside that repository — never committed, because dv adds
// the directory to .git/info/exclude on first run.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"hash/fnv"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"dv/internal/gitx"
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
		port    = flag.Int("port", 0, "port to listen on (0 picks one from the repository's path)")
		host    = flag.String("host", "127.0.0.1", "address to bind")
		dir     = flag.String("C", ".", "repository directory")
		noOpen  = flag.Bool("no-open", false, "do not open a browser")
		version = flag.Bool("version", false, "print version and exit")
	)
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: dv [flags]\n       dv reset [-y]\n\n"+
			"Review the current repository's diff in a browser. reset deletes the\n"+
			"review's comments and viewed marks, to start it over.\n\nflags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *version {
		fmt.Println("dv " + versionString)
		return nil
	}
	switch cmd := flag.Arg(0); cmd {
	case "":
	case "reset":
		return reset(*dir, flag.Args()[1:])
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
	excludeNotes(repo)

	ix := symindex.New(repo.Root, repo)
	ix.BuildAsync()

	if !server.AssetsBuilt() {
		return fmt.Errorf("this binary has no UI bundle in it - run `make build` (needs Node) and try again")
	}

	srv := server.New(repo, st, vw, ix)
	ln, err := listen(*host, *port, repo.Root)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("http://%s", ln.Addr().String())

	fmt.Printf("\n  \033[1m%s\033[0m — reviewing %s\n", repo.Name(), repo.Root)
	fmt.Printf("  \033[1;32m%s\033[0m\n", url)
	fmt.Printf("  comments → %s\n\n  ctrl-c to stop\n\n", st.Path())

	if !*noOpen {
		openBrowser(url)
	}

	httpSrv := &http.Server{
		Handler: srv.Handler(),
		// No write timeout: /api/ask streams a model's answer and can legitimately
		// run for minutes. Read timeouts still bound a stuck client.
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       60 * time.Second,
	}
	return httpSrv.Serve(ln)
}

const versionString = "0.1.0"

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
