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
	"sync/atomic"
	"syscall"
	"time"

	"dv/internal/codex"
	"dv/internal/gitx"
	"dv/internal/hub"
	"dv/internal/permit"
	"dv/internal/server"
	"dv/internal/store"
	"dv/internal/update"
)

func main() {
	// Not for the sessions dv starts, nor the next restart.
	os.Unsetenv(restartEnv)
	os.Unsetenv(update.TrialEnv)
	if trial {
		time.AfterFunc(time.Minute, func() { os.Exit(1) }) // should what started it not stop it
	}
	err := run()
	// Only now, with run's deferred cleanup done: sessions stopped, pages closed
	// and the prompts' routing to this dv taken down.
	if again := (errRestart{}); errors.As(err, &again) {
		fmt.Printf("  restarting dv\n\n")
		err = restart(again.addr)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "dv:", err)
		os.Exit(1)
	}
}

// restartEnv carries a restarting dv's address to the one it starts, which
// takes the same address, since its pages are open on it, and opens no browser.
const restartEnv = "DV_RESTART_ADDR"

var restartAddr = os.Getenv(restartEnv)

// trial is a dv started by a running one to see that it starts and serves
// before that one hands over (update.Try). It listens on a port of its own and
// touches nothing another dv shares: where Claude Code's prompts are routed,
// the hooks, a browser.
var trial = os.Getenv(update.TrialEnv) != ""

const trialAddr = "127.0.0.1:0"

// errRestart is how serve says dv is to run again; addr is where it listened.
type errRestart struct{ addr string }

func (errRestart) Error() string { return "restart" }

func run() error {
	var (
		port        = flag.Int("port", 0, "port to listen on (0 picks one from the repository's path)")
		host        = flag.String("host", "127.0.0.1", "address to bind")
		dir         = flag.String("C", ".", "repository directory")
		noOpen      = flag.Bool("no-open", false, "do not open a browser")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: dv [flags]\n       dv hub [-port n] [-host addr] [-no-open]\n       dv reset [-y]\n\n"+
			"Review the current repository's diff in a browser (outside git, read the\n"+
			"folder's files). hub serves many folders from one port, with a page to\n"+
			"add, clone and open them. reset deletes the\n"+
			"review's comments and viewed marks, to start it over.\n\nflags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	codex.ClientVersion = version
	server.Version = version

	if *showVersion {
		fmt.Println("dv " + version)
		return nil
	}
	switch cmd := flag.Arg(0); cmd {
	case "":
	case "hub":
		return runHub(flag.Args()[1:])
	case "reset":
		return reset(*dir, flag.Args()[1:])
	case "claude":
		return claude(flag.Args()[1:])
	default:
		return fmt.Errorf("unknown command %q (dv -h lists them)", cmd)
	}

	if !server.AssetsBuilt() {
		return errNoBundle
	}
	repo, err := gitx.Open(*dir)
	if err != nil {
		return err
	}
	// Open in another dv, a hub's included, it is opened there rather than
	// twice: each would take Claude Code's prompts to be its own.
	if s, ok := store.Announced(repo.Root); ok && !trial && server.Answers(s.URL, repo.Root) {
		fmt.Printf("\n  \033[1m%s\033[0m is already open in dv\n  \033[1;32m%s\033[0m\n\n", repo.Name(), s.URL)
		if !*noOpen {
			openBrowser(s.URL)
		}
		return nil
	}
	user, err := store.OpenUserPrefs()
	if err != nil {
		return err
	}
	if !trial {
		healHooks()
	}

	srv, err := server.Open(repo.Root, user)
	if err != nil {
		return err
	}
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
	fmt.Printf("  comments → %s\n\n  ctrl-c to stop\n\n", srv.CommentsPath())

	if !*noOpen {
		openBrowser(url)
	}

	if trial {
		fmt.Println(update.TrialMark + ln.Addr().String())
	} else if unannounce, err := store.Announce(repo.Root, url); err != nil {
		fmt.Fprintln(os.Stderr, "dv: warning: Claude Code's prompts cannot find this dv:", err)
	} else {
		defer unannounce()
	}
	return serve(ln, srv.Handler(""))
}

// hubPort is the hub's address, just under the ports lone dvs take.
const hubPort = 41000

// runHub serves the folders on the user's hub list from one port.
func runHub(args []string) error {
	fl := flag.NewFlagSet("dv hub", flag.ExitOnError)
	port := fl.Int("port", hubPort, "port to listen on")
	host := fl.String("host", "127.0.0.1", "address to bind (0.0.0.0 to reach it from other devices)")
	noOpen := fl.Bool("no-open", false, "do not open a browser")
	fl.Parse(args)

	if !server.AssetsBuilt() {
		return errNoBundle
	}
	user, err := store.OpenUserPrefs()
	if err != nil {
		return err
	}
	folders, err := store.OpenFolders()
	if err != nil {
		return err
	}
	addr := net.JoinHostPort(*host, strconv.Itoa(*port))
	if trial {
		addr = trialAddr
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if url := "http://" + addr; !trial && hubAnswers(url) {
			fmt.Printf("\n  dv hub is already running\n  \033[1;32m%s\033[0m\n\n", url)
			if !*noOpen {
				openBrowser(url)
			}
			return nil
		}
		return fmt.Errorf("%w (-port picks another)", err)
	}
	url := "http://" + ln.Addr().String()
	if !trial {
		healHooks()
	}

	h := hub.New(url, user, folders)
	defer h.Close()
	fmt.Printf("\n  \033[1mdv hub\033[0m — %s\n", count(len(folders.List()), "folder"))
	fmt.Printf("  \033[1;32m%s\033[0m\n\n  ctrl-c to stop\n\n", url)
	if !*noOpen {
		openBrowser(url)
	}
	// Asked for nothing but its page, a trial opens no folder, so announces
	// none: where Claude Code's prompts go stays with the hub running.
	if trial {
		fmt.Println(update.TrialMark + ln.Addr().String())
	}
	return serve(ln, h.Handler())
}

func hubAnswers(url string) bool {
	c := http.Client{Timeout: time.Second}
	resp, err := c.Get(url + "/api/hub/folders")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

var errNoBundle = errors.New("this binary has no UI bundle in it - run `make build` (needs Node) and try again")

// serve runs until interrupted. Stopping has to return through the caller's
// deferred unannounce, or the next hook goes looking for a dv that is not there.
func serve(ln net.Listener, handler http.Handler) error {
	httpSrv := &http.Server{
		Handler: handler,
		// No write timeout: a session's events stream for as long as the page is
		// open, and a permission prompt waits on the reader. Read timeouts still
		// bound a stuck client.
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var again atomic.Bool
	go func() {
		select {
		case <-ctx.Done():
		case <-server.Restart:
			again.Store(true)
		}
		httpSrv.Close()
	}()
	if err := httpSrv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	if again.Load() {
		return errRestart{ln.Addr().String()}
	}
	return nil
}

// version is stamped in by release builds (see .goreleaser.yaml).
var version = "dev"

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
	if trial {
		return net.Listen("tcp", trialAddr)
	}
	if restartAddr != "" {
		return net.Listen("tcp", restartAddr)
	}
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
	if restartAddr != "" || trial {
		return
	}
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
