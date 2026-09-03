package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestGitHubJWKCacheBoundsExpiryAndRefreshesUnknownKey(t *testing.T) {
	now := time.Now()
	calls := 0
	cache := newGitHubJWKCache()
	cache.now = func() time.Time { return now }
	cache.fetch = func(_ *http.Request, _ string) (map[string]interface{}, time.Duration, error) {
		calls++
		return map[string]interface{}{"kid-1": "key"}, time.Hour, nil
	}
	request := httptest.NewRequest(http.MethodGet, "https://control.example.test", nil)
	if _, err := cache.get(request, "https://token.actions.githubusercontent.com/.well-known/jwks", false); err != nil || calls != 1 {
		t.Fatalf("initial cache load err=%v calls=%d", err, calls)
	}
	if _, err := cache.get(request, "https://token.actions.githubusercontent.com/.well-known/jwks", false); err != nil || calls != 1 {
		t.Fatalf("fresh cache should avoid a second fetch: err=%v calls=%d", err, calls)
	}
	if _, err := cache.get(request, "https://token.actions.githubusercontent.com/.well-known/jwks", true); err != nil || calls != 2 {
		t.Fatalf("unknown kid refresh err=%v calls=%d", err, calls)
	}
	if _, err := cache.get(request, "https://token.actions.githubusercontent.com/.well-known/jwks", true); err != nil || calls != 2 {
		t.Fatalf("random kid flood bypassed refresh cooldown: err=%v calls=%d", err, calls)
	}
	cache.fetch = func(_ *http.Request, _ string) (map[string]interface{}, time.Duration, error) {
		return nil, 0, errors.New("outage")
	}
	now = now.Add(githubJWKCacheMaxTTL + time.Second)
	if _, err := cache.get(request, "https://token.actions.githubusercontent.com/.well-known/jwks", false); err == nil {
		t.Fatal("expired JWKS cache was used during an issuer outage")
	}
}

func TestGitHubJWKCacheCoalescesConcurrentFetches(t *testing.T) {
	cache := newGitHubJWKCache()
	started, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	calls := 0
	cache.fetch = func(_ *http.Request, _ string) (map[string]interface{}, time.Duration, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		close(started)
		<-release
		return map[string]interface{}{"kid": "key"}, time.Minute, nil
	}
	request := httptest.NewRequest(http.MethodGet, "https://control.example.test", nil)
	results := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := cache.get(request, "https://token.actions.githubusercontent.com/.well-known/jwks", true)
			results <- err
		}()
	}
	<-started
	close(release)
	if err := <-results; err != nil {
		t.Fatal(err)
	}
	if err := <-results; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("concurrent JWKS fetches = %d, want 1", calls)
	}
}
