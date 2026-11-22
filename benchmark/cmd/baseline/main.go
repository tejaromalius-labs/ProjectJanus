// Command baseline is a minimal reverse proxy running one classic policy
// (rr, lc, p2c or pewma). It uses the same upstream transport settings as the
// Janus proxy and has no circuit breakers or retries: the algorithm as-is.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"

	"github.com/tejaromalius-labs/ProjectJanus/benchmark/internal/policy"
)

// observing measures time to response headers (as Janus does) and reports
// it to the policy, including failed attempts.
type observing struct {
	base    http.RoundTripper
	backend *policy.Backend
	policy  policy.Policy
}

func (t *observing) RoundTrip(r *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := t.base.RoundTrip(r)
	t.policy.Observe(t.backend, time.Since(start))
	return resp, err
}

func main() {
	listen := flag.String("listen", ":18080", "listen address")
	name := flag.String("policy", "rr", "rr, lc, p2c or pewma")
	list := flag.String("backends", "", "id=host:port,...")
	flag.Parse()

	var backends []*policy.Backend
	for _, entry := range strings.Split(*list, ",") {
		id, addr, ok := strings.Cut(strings.TrimSpace(entry), "=")
		if !ok || id == "" || addr == "" {
			log.Fatalf("invalid -backends entry %q, want id=host:port", entry)
		}
		backends = append(backends, &policy.Backend{ID: id, Addr: addr})
	}
	pol, err := policy.New(*name, backends)
	if err != nil {
		log.Fatal(err)
	}

	// Mirrors linucb-proxy/internal/proxy/proxy.go transport settings.
	base := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          1024,
		MaxIdleConnsPerHost:   256,
		IdleConnTimeout:       60 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
		DialContext:           (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
	}
	proxies := make(map[*policy.Backend]*httputil.ReverseProxy, len(backends))
	for _, b := range backends {
		addr := b.Addr
		proxies[b] = &httputil.ReverseProxy{
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.Out.URL.Scheme = "http"
				pr.Out.URL.Host = addr
				pr.Out.Host = addr
			},
			Transport: &observing{base: base, backend: b, policy: pol},
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				w.WriteHeader(http.StatusBadGateway)
			},
		}
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			fmt.Fprint(w, "ok")
			return
		}
		b := pol.Pick()
		b.Acquire()
		defer b.Release()
		proxies[b].ServeHTTP(w, r)
	})
	log.Printf("baseline policy=%s listening on %s", *name, *listen)
	log.Fatal(http.ListenAndServe(*listen, handler))
}
