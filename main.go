package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
)

func main() {
	port := flag.Int("port", 4000, "port to listen on")
	target := flag.String("target", "", "API to forward requests to, e.g. https://api.anthropic.com")
	flag.Parse()

	if *target == "" {
		fmt.Fprintln(os.Stderr, "error: -target is required, e.g. -target https://api.anthropic.com")
		os.Exit(2)
	}
	targetURL, err := url.Parse(*target)
	if err != nil || targetURL.Scheme == "" || targetURL.Host == "" {
		fmt.Fprintf(os.Stderr, "error: invalid -target %q, expected something like https://api.anthropic.com\n", *target)
		os.Exit(2)
	}

	proxy := &httputil.ReverseProxy{
		// Runs before each request is sent: point it at the real API.
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(targetURL)
		},
		// Runs when the real API's response comes back, before it is sent to the app.
		ModifyResponse: func(resp *http.Response) error {
			log.Printf("%s %s -> %d", resp.Request.Method, resp.Request.URL, resp.StatusCode)
			return nil
		},
	}

	// Every request reaches this handler first. Later it will decide whether to
	// answer from a recording or forward the request to the real API.
	handler := func(w http.ResponseWriter, r *http.Request) {
		log.Printf("incoming: %s %s", r.Method, r.URL.Path)
		proxy.ServeHTTP(w, r)
	}

	// 127.0.0.1 means only this machine can connect; the proxy forwards API keys.
	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	log.Printf("forwarding http://%s -> %s", addr, targetURL)
	log.Fatal(http.ListenAndServe(addr, http.HandlerFunc(handler)))
}
