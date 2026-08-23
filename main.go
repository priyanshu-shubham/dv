// dv opens a local diff reviewer for the git repository you run it in. It
// serves a browser UI on loopback, and any comments you leave are written to
// .dv/comments.json inside that repository — never committed, because dv adds
// the directory to .git/info/exclude on first run.
package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
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
		port    = flag.Int("port", 0, "port to listen on (0 scans upward from the default)")
		host    = flag.String("host", "127.0.0.1", "address to bind")
		dir     = flag.String("C", ".", "repository directory")
		noOpen  = flag.Bool("no-open", false, "do not open a browser")
		version = flag.Bool("version", false, "print version and exit")
	)
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: dv [flags]\n\nReview the current repository's diff in a browser.\n\nflags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *version {
		fmt.Println("dv " + versionString)
		return nil
	}

	repo, err := gitx.Open(*dir)
	if err != nil {
		return err
	}
	st, err := store.Open(repo.Root, repo.Name())
	if err != nil {
		return err
	}
	if added, err := store.EnsureExcluded(repo.GitDir); err != nil {
		fmt.Fprintln(os.Stderr, "dv: warning: could not update .git/info/exclude:", err)
	} else if added {
		fmt.Println("dv: added /.dv/ to .git/info/exclude so review notes stay out of git")
	}

	ix := symindex.New(repo.Root, repo)
	ix.BuildAsync()

	if !server.AssetsBuilt() {
		return fmt.Errorf("this binary has no UI bundle in it - run `make build` (needs Node) and try again")
	}

	srv := server.New(repo, st, ix)
	ln, err := listen(*host, *port)
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

// basePort is the first port dv tries. Scanning upward from a fixed base rather
// than asking the kernel for any free port means consecutive runs land on the
// same URL — so a bookmarked or already-open tab keeps working, and you are not
// hunting for a new number every time.
const basePort = 41100

// portScanRange bounds the search before giving up.
const portScanRange = 200

func listen(host string, port int) (net.Listener, error) {
	if port != 0 {
		return net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	}
	var firstErr error
	for p := basePort; p < basePort+portScanRange; p++ {
		ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(p)))
		if err == nil {
			return ln, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, fmt.Errorf("no free port between %d and %d: %w", basePort, basePort+portScanRange-1, firstErr)
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
