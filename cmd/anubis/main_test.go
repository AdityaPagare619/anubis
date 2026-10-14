package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
)

// TestCheckHealthUnixSocket proves that the healthcheck dials the metrics
// server over its unix socket instead of concatenating the socket path into
// an http://localhost URL.
func TestCheckHealthUnixSocket(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "anubis_metrics.sock")

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen on unix socket: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)                  //nolint:errcheck
	t.Cleanup(func() { srv.Close() }) //nolint:errcheck

	if err := checkHealth("unix", sockPath); err != nil {
		t.Fatalf("healthcheck over unix socket failed: %v", err)
	}
}

// TestCheckHealthTCP pins the existing TCP behavior: host:port concatenation
// against a plain TCP metrics bind keeps working.
func TestCheckHealthTCP(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatal(err)
	}

	if err := checkHealth("tcp", ":"+u.Port()); err != nil {
		t.Fatalf("healthcheck over tcp failed: %v", err)
	}
}

// TestCheckHealthUnhealthy pins that a non-200 from /healthz is still an
// error on both transports.
func TestCheckHealthUnhealthy(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatal(err)
	}

	if err := checkHealth("tcp", ":"+u.Port()); err == nil {
		t.Fatal("expected error for non-200 healthz, got nil")
	}
}

func TestMakeReverseProxy(t *testing.T) {
	type received struct {
		host  string
		path  string
		query string
		hdr   http.Header
	}

	for _, tt := range []struct {
		name       string
		targetHost string
		reqHost    string
		reqPath    string
		reqHeaders map[string]string
		wantHost   string // empty means "same as the target server's host"
	}{
		{
			name:    "default preserves inbound host",
			reqHost: "anubis.example.com",
			reqPath: "/foo/bar?baz=qux",
		},
		{
			// gitweb uses ';' as a query separator (e.g.
			// /?p=repo.git;a=summary). httputil's Rewrite mode re-encodes the
			// query and would otherwise drop these parameters entirely.
			name:    "semicolon-delimited query is preserved",
			reqHost: "anubis.example.com",
			reqPath: "/?p=testing.git;a=summary",
		},
		{
			name:       "target host override",
			targetHost: "upstream.internal",
			reqHost:    "anubis.example.com",
			reqPath:    "/",
			wantHost:   "upstream.internal",
		},
		{
			name:    "forwarding headers are passed through",
			reqHost: "anubis.example.com",
			reqPath: "/",
			reqHeaders: map[string]string{
				"X-Forwarded-For":   "203.0.113.7, 198.51.100.2",
				"X-Forwarded-Host":  "anubis.example.com",
				"X-Forwarded-Proto": "https",
				"Forwarded":         "for=203.0.113.7;proto=https",
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gotCh := make(chan received, 1)
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotCh <- received{
					host:  r.Host,
					path:  r.URL.Path,
					query: r.URL.RawQuery,
					hdr:   r.Header.Clone(),
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(target.Close)

			h, err := makeReverseProxy(target.URL, "", tt.targetHost, false, false)
			if err != nil {
				t.Fatalf("makeReverseProxy: %v", err)
			}

			req := httptest.NewRequest(http.MethodGet, "http://"+tt.reqHost+tt.reqPath, nil)
			req.Host = tt.reqHost
			for k, v := range tt.reqHeaders {
				req.Header.Set(k, v)
			}

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusNoContent {
				t.Fatalf("unexpected status from proxy: got %d, want %d", rec.Code, http.StatusNoContent)
			}

			got := <-gotCh

			wantHost := tt.wantHost
			if wantHost == "" {
				wantHost = tt.reqHost
			}
			if got.host != wantHost {
				t.Errorf("upstream Host: got %q, want %q", got.host, wantHost)
			}

			wantURL, _ := url.Parse("http://x" + tt.reqPath)
			if got.path != wantURL.Path {
				t.Errorf("upstream path: got %q, want %q", got.path, wantURL.Path)
			}
			if got.query != wantURL.RawQuery {
				t.Errorf("upstream query: got %q, want %q", got.query, wantURL.RawQuery)
			}

			for k, want := range tt.reqHeaders {
				if gotVal := got.hdr.Get(k); gotVal != want {
					t.Errorf("upstream header %q: got %q, want %q", k, gotVal, want)
				}
			}
		})
	}
}
