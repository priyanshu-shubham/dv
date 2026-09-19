// dv-gchat-relay puts dv in Google Chat. It is the endpoint of a Google Chat
// app, which dv hubs connect out to: what someone writes to the app goes to
// their hub, and what the hub sends back goes in Google Chat as the app's.
// See README.md for setting it up, on Cloud Run.
//
// It is set up by its environment:
//
//	DV_RELAY_ALLOW     who may use it: emails, and @domains for anyone at one,
//	                   comma-separated. Required.
//	DV_RELAY_AUDIENCE  what Google Chat's requests are for, as the app's
//	                   settings say: the endpoint's URL, or the project's number.
//	                   Required, unless DV_RELAY_UNVERIFIED=1 lets anyone
//	                   pretend to be Google Chat, to try the relay out.
//	DV_RELAY_STORE     "firestore", the default, keeps hubs in the project's
//	                   Firestore; "memory" forgets them on stopping.
//	GOOGLE_CLOUD_PROJECT  the project, where the metadata server does not say.
//	PORT               where to listen, 8080 by default.
//
// It keeps its hubs' events in memory, so it runs as one instance.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	log.SetFlags(0)
	allowed, err := allowList(os.Getenv("DV_RELAY_ALLOW"))
	if err != nil {
		log.Fatal(err)
	}
	var verify func(*http.Request) error
	switch audience := strings.TrimSpace(os.Getenv("DV_RELAY_AUDIENCE")); {
	case audience != "":
		verify = newVerifier(audience).check
	case os.Getenv("DV_RELAY_UNVERIFIED") == "1":
		log.Print("taking requests to /chat as Google Chat's without checking them")
	default:
		log.Fatal("DV_RELAY_AUDIENCE is required: the app's endpoint URL, or its project's number, as its settings in Google Cloud say")
	}
	var store Store
	switch kind := os.Getenv("DV_RELAY_STORE"); kind {
	case "", "firestore":
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		project, err := projectID(ctx)
		cancel()
		if err != nil {
			log.Fatalf("no project for Firestore: set GOOGLE_CLOUD_PROJECT, or DV_RELAY_STORE=memory (%v)", err)
		}
		store = newFirestore(project, newTokens("https://www.googleapis.com/auth/datastore"))
	case "memory":
		store = newMemory()
	default:
		log.Fatalf("DV_RELAY_STORE is %q: firestore or memory", kind)
	}

	r := newRelay(store, newGoogleChat(), allowed, verify)
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	srv := &http.Server{Addr: ":" + port, Handler: r.Handler(), ReadHeaderTimeout: 10 * time.Second}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()
	log.Printf("dv-gchat-relay listening on :%s", port)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

// allowList reads DV_RELAY_ALLOW: "ada@example.com, @example.org".
func allowList(s string) (func(email string) bool, error) {
	var emails, domains []string
	for _, a := range strings.Split(strings.ToLower(s), ",") {
		switch a = strings.TrimSpace(a); {
		case a == "":
		case strings.HasPrefix(a, "@"):
			domains = append(domains, a)
		case strings.Contains(a, "@"):
			emails = append(emails, a)
		default:
			return nil, errors.New("DV_RELAY_ALLOW has " + a + ": an email, or @ and a domain")
		}
	}
	if len(emails)+len(domains) == 0 {
		return nil, errors.New("DV_RELAY_ALLOW is required: the emails, and @domains, of those who may use the relay")
	}
	return func(email string) bool {
		email = strings.ToLower(strings.TrimSpace(email))
		if email == "" {
			return false
		}
		for _, e := range emails {
			if email == e {
				return true
			}
		}
		for _, d := range domains {
			if strings.HasSuffix(email, d) {
				return true
			}
		}
		return false
	}, nil
}

func projectID(ctx context.Context) (string, error) {
	if p := os.Getenv("GOOGLE_CLOUD_PROJECT"); p != "" {
		return p, nil
	}
	var p string
	err := fromMetadata(ctx, &http.Client{Timeout: 5 * time.Second}, "project/project-id", &p)
	return p, err
}
