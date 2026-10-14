package main

import (
	"flag"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
)

// TestDeprecatedUseSimplifiedExplanationWarning pins when the
// USE_SIMPLIFIED_EXPLANATION deprecation warning fires: only when the flag
// was explicitly set (CLI or env), never from its default value.
func TestDeprecatedUseSimplifiedExplanationWarning(t *testing.T) {
	// keep the ambient environment from leaking into the test
	oldEnv, hadEnv := os.LookupEnv("USE_SIMPLIFIED_EXPLANATION")
	os.Unsetenv("USE_SIMPLIFIED_EXPLANATION") //nolint:errcheck
	t.Cleanup(func() {
		if hadEnv {
			os.Setenv("USE_SIMPLIFIED_EXPLANATION", oldEnv) //nolint:errcheck
		}
	})

	strptr := func(s string) *string { return &s }

	for _, tt := range []struct {
		name   string
		args   []string
		envVal *string // set USE_SIMPLIFIED_EXPLANATION in the environment
		want   bool
	}{
		{name: "default (nothing set) does not warn", want: false},
		{name: "explicit true warns", args: []string{"-use-simplified-explanation=true"}, want: true},
		{name: "explicit false warns", args: []string{"-use-simplified-explanation=false"}, want: true},
		{name: "env true warns", envVal: strptr("true"), want: true},
		{name: "env false warns", envVal: strptr("false"), want: true},
		{name: "env empty (but present) warns", envVal: strptr(""), want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envVal != nil {
				t.Setenv("USE_SIMPLIFIED_EXPLANATION", *tt.envVal)
			}
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			fs.Bool("use-simplified-explanation", true, "deprecated")
			if err := fs.Parse(tt.args); err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got := deprecatedUseSimplifiedExplanation(fs); got != tt.want {
				t.Errorf("deprecatedUseSimplifiedExplanation() = %v, want %v", got, tt.want)
			}
		})
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
