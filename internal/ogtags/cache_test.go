package ogtags

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/TecharoHQ/anubis/lib/config"
	"github.com/TecharoHQ/anubis/lib/store"
	"github.com/TecharoHQ/anubis/lib/store/memory"
)

func TestCacheReturnsDefault(t *testing.T) {
	want := map[string]string{
		"og:title":       "Foo bar",
		"og:description": "The best website ever made!!!1!",
	}
	cache := NewOGTagCache("", config.OpenGraph{
		Enabled:      true,
		TimeToLive:   time.Minute,
		ConsiderHost: false,
		Override:     want,
	}, memory.New(t.Context()), TargetOptions{})

	u, err := url.Parse("https://anubis.techaro.lol")
	if err != nil {
		t.Fatal(err)
	}

	result, err := cache.GetOGTags(t.Context(), u, "anubis.techaro.lol")
	if err != nil {
		t.Fatal(err)
	}

	for k, v := range want {
		t.Run(k, func(t *testing.T) {
			if got := result[k]; got != v {
				t.Logf("want: tags[%q] = %q", k, v)
				t.Logf("got:  tags[%q] = %q", k, got)
				t.Error("invalid result from function")
			}
		})
	}
}

func TestCheckCache(t *testing.T) {
	cache := NewOGTagCache("http://example.com", config.OpenGraph{
		Enabled:      true,
		TimeToLive:   time.Minute,
		ConsiderHost: false,
	}, memory.New(t.Context()), TargetOptions{})

	// Set up test data
	urlStr := "http://example.com/page"
	expectedTags := map[string]string{
		"og:title":       "Test Title",
		"og:description": "Test Description",
	}
	cacheKey := cache.generateCacheKey(urlStr, "example.com")

	// Test cache miss
	tags := cache.checkCache(t.Context(), cacheKey)
	if tags != nil {
		t.Errorf("expected nil tags on cache miss, got %v", tags)
	}

	// Manually add to cache
	if err := cache.cache.Set(t.Context(), cacheKey, expectedTags, time.Minute); err != nil {
		t.Fatal(err)
	}

	// Test cache hit
	tags = cache.checkCache(t.Context(), cacheKey)
	if tags == nil {
		t.Fatal("expected non-nil tags on cache hit, got nil")
	}

	for key, expectedValue := range expectedTags {
		if value, ok := tags[key]; !ok || value != expectedValue {
			t.Errorf("expected %s: %s, got: %s", key, expectedValue, value)
		}
	}
}

func TestGetOGTags(t *testing.T) {
	var loadCount int // Counter to track how many times the test route is loaded

	// Create a test server to serve a sample HTML page with OG tags
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		loadCount++
		if loadCount > 1 {
			t.Fatalf("Test route loaded more than once, cache failed")
		}
		w.Header().Set("Content-Type", "text/html")
		// nolint:errcheck
		w.Write([]byte(`
			<!DOCTYPE html>
			<html>
			<head>
				<meta property="og:title" content="Test Title" />
				<meta property="og:description" content="Test Description" />
				<meta property="og:image" content="http://example.com/image.jpg" />
			</head>
			<body>
				<p>Hello, world!</p>
			</body>
			</html>
		`))
	}))
	defer ts.Close()

	// Create an instance of OGTagCache with a short TTL for testing
	cache := NewOGTagCache(ts.URL, config.OpenGraph{
		Enabled:      true,
		TimeToLive:   time.Minute,
		ConsiderHost: false,
	}, memory.New(t.Context()), TargetOptions{})

	// Parse the test server URL
	parsedURL, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("failed to parse test server URL: %v", err)
	}

	// Test fetching OG tags from the test server
	// Pass the host from the parsed test server URL
	ogTags, err := cache.GetOGTags(t.Context(), parsedURL, parsedURL.Host)
	if err != nil {
		t.Fatalf("failed to get OG tags: %v", err)
	}

	// Verify the fetched OG tags
	expectedTags := map[string]string{
		"og:title":       "Test Title",
		"og:description": "Test Description",
		"og:image":       "http://example.com/image.jpg",
	}

	for key, expectedValue := range expectedTags {
		if value, ok := ogTags[key]; !ok || value != expectedValue {
			t.Errorf("expected %s: %s, got: %s", key, expectedValue, value)
		}
	}

	// Test fetching OG tags from the cache
	// Pass the host from the parsed test server URL
	ogTags, err = cache.GetOGTags(t.Context(), parsedURL, parsedURL.Host)
	if err != nil {
		t.Fatalf("failed to get OG tags from cache: %v", err)
	}

	// Test fetching OG tags from the cache (3rd time)
	// Pass the host from the parsed test server URL
	newOgTags, err := cache.GetOGTags(t.Context(), parsedURL, parsedURL.Host)
	if err != nil {
		t.Fatalf("failed to get OG tags from cache: %v", err)
	}

	// Verify the cached OG tags
	for key, expectedValue := range expectedTags {
		if value, ok := ogTags[key]; !ok || value != expectedValue {
			t.Errorf("expected %s: %s, got: %s", key, expectedValue, value)
		}

		initialValue := ogTags[key]
		cachedValue, ok := newOgTags[key]
		if !ok || initialValue != cachedValue {
			t.Errorf("Cache does not line up: expected %s: %s, got: %s", key, initialValue, cachedValue)
		}
	}

	t.Run("metadata does not authorize assets", func(t *testing.T) {
		if _, err := cache.cache.Underlying.Get(t.Context(), "ogtags:allow:example.com/image.jpg"); !errors.Is(err, store.ErrNotFound) {
			t.Fatal("metadata created an authorization cache entry")
		}
	})
}

// TestGetOGTagsWithHostConsideration tests the behavior of the cache with and without host consideration and for multiple hosts in a theoretical setup.
func TestGetOGTagsWithHostConsideration(t *testing.T) {
	var loadCount int // Counter to track how many times the test route is loaded

	// Create a test server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		loadCount++ // Increment counter on each request to the server
		w.Header().Set("Content-Type", "text/html")
		// nolint:errcheck
		w.Write([]byte(`
			<!DOCTYPE html>
			<html>
			<head>
				<meta property="og:title" content="Test Title" />
				<meta property="og:description" content="Test Description" />
			</head>
			<body><p>Content</p></body>
			</html>
		`))
	}))
	defer ts.Close()

	parsedURL, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("failed to parse test server URL: %v", err)
	}

	expectedTags := map[string]string{
		"og:title":       "Test Title",
		"og:description": "Test Description",
	}

	testCases := []struct {
		name     string
		requests []struct {
			host              string
			expectedLoadCount int
		}
		ogCacheConsiderHost bool // Expected load count *after* this request
	}{
		{
			name:                "Host Not Considered - Same Host",
			ogCacheConsiderHost: false,
			requests: []struct {
				host              string
				expectedLoadCount int
			}{
				{"host1", 1}, // First request, miss
				{"host1", 1}, // Second request, same host, hit (host ignored)
			},
		},
		{
			name:                "Host Not Considered - Different Host",
			ogCacheConsiderHost: false,
			requests: []struct {
				host              string
				expectedLoadCount int
			}{
				{"host1", 1}, // First request, miss
				{"host2", 1}, // Second request, different host, hit (host ignored)
			},
		},
		{
			name:                "Host Considered - Same Host",
			ogCacheConsiderHost: true,
			requests: []struct {
				host              string
				expectedLoadCount int
			}{
				{"host1", 1}, // First request, miss
				{"host1", 1}, // Second request, same host, hit
			},
		},
		{
			name:                "Host Considered - Different Host",
			ogCacheConsiderHost: true,
			requests: []struct {
				host              string
				expectedLoadCount int
			}{
				{"host1", 1}, // First request, miss
				{"host2", 2}, // Second request, different host, miss
				{"host2", 2}, // Third request, same as second, hit
				{"host1", 2}, // Fourth request, same as first, hit
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			loadCount = 0 // Reset load count for each test case
			cache := NewOGTagCache(ts.URL, config.OpenGraph{
				Enabled:      true,
				TimeToLive:   time.Minute,
				ConsiderHost: tc.ogCacheConsiderHost,
			}, memory.New(t.Context()), TargetOptions{})

			for i, req := range tc.requests {
				ogTags, err := cache.GetOGTags(t.Context(), parsedURL, req.host)
				if err != nil {
					t.Errorf("Request %d (host: %s): unexpected error: %v", i+1, req.host, err)
					continue // Skip further checks for this request if error occurred
				}

				// Verify tags are correct (should always be the same in this setup)
				if !reflect.DeepEqual(ogTags, expectedTags) {
					t.Errorf("Request %d (host: %s): expected tags %v, got %v", i+1, req.host, expectedTags, ogTags)
				}

				// Verify the load count to check cache hit/miss behavior
				if loadCount != req.expectedLoadCount {
					t.Errorf("Request %d (host: %s): expected load count %d, got %d (cache hit/miss mismatch)", i+1, req.host, req.expectedLoadCount, loadCount)
				}
			}
		})
	}
}

// TestGetOGTagsBenignFetchFailures pins the behavior behind
// https://github.com/TecharoHQ/anubis/issues/1981: fetch failures that are
// expected in production (slow backends, huge pages, missing Content-Type,
// aborted requests) must not surface as errors from GetOGTags, because the
// challenge page renders fine with ogTags=nil and RenderIndex logs every
// returned error at ERROR.
func TestGetOGTagsBenignFetchFailures(t *testing.T) {
	// newCache builds a cache whose fetch target is the test server; the
	// request URL path is appended to it by getTarget.
	newCache := func(t *testing.T, target string) *OGTagCache {
		return NewOGTagCache(target, config.OpenGraph{
			Enabled:      true,
			TimeToLive:   time.Minute,
			ConsiderHost: false,
		}, memory.New(t.Context()), TargetOptions{})
	}

	fetchOK := func(t *testing.T, ts *httptest.Server, ctx context.Context) {
		t.Helper()
		u, err := url.Parse(ts.URL)
		if err != nil {
			t.Fatal(err)
		}
		tags, err := newCache(t, ts.URL).GetOGTags(ctx, u, "")
		if err != nil {
			t.Errorf("benign failure returned an error: %v", err)
		}
		if tags != nil {
			t.Errorf("expected nil tags, got %v", tags)
		}
	}

	t.Run("missing Content-Type", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// hijack so the response carries no Content-Type at all
			// (net/http would otherwise sniff one)
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Error("server does not support hijacking")
				return
			}
			conn, bufrw, err := hj.Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			defer conn.Close() //nolint:errcheck
			body := "<html><head></head><body>no content type</body></html>"
			fmt.Fprintf(bufrw, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body) //nolint:errcheck
			bufrw.Flush()                                                                                               //nolint:errcheck
		}))
		defer ts.Close()

		fetchOK(t, ts, t.Context())
	})

	t.Run("oversized body", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			// 9 MiB > maxContentLength (8 MiB)
			chunk := strings.Repeat("x", 1<<20)
			for i := 0; i < 9; i++ {
				if _, err := io.WriteString(w, chunk); err != nil {
					return
				}
			}
		}))
		defer ts.Close()

		fetchOK(t, ts, t.Context())
	})

	t.Run("client disconnect (context canceled)", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<html></html>")) //nolint:errcheck
		}))
		defer ts.Close()

		ctx, cancel := context.WithCancel(t.Context())
		cancel() // simulate the operator disconnecting mid-challenge

		fetchOK(t, ts, ctx)
	})

	t.Run("slow backend (deadline exceeded)", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(6 * time.Second) // longer than the 5s fetch timeout
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<html></html>")) //nolint:errcheck
		}))
		defer ts.Close()

		fetchOK(t, ts, t.Context())
	})
}
