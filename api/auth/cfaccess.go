package auth

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// CFAccessClaims holds the validated claims from a CF Access JWT.
type CFAccessClaims struct {
	Email string `json:"email"`
	jwt.RegisteredClaims
}

// jwksKey is a single RSA public key from the JWKS endpoint.
type jwksKey struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

type jwksResponse struct {
	Keys []jwksKey `json:"keys"`
}

type cachedJWKS struct {
	mu        sync.RWMutex
	keys      map[string]*rsa.PublicKey
	certsURL  string
	fetchedAt time.Time
	ttl       time.Duration
	httpGet   func(url string) (*http.Response, error) // for testing
}

func newCachedJWKS(certsURL string) *cachedJWKS {
	return &cachedJWKS{
		keys:     make(map[string]*rsa.PublicKey),
		certsURL: certsURL,
		ttl:      5 * time.Minute,
		httpGet:  http.Get,
	}
}

func (c *cachedJWKS) getKey(kid string) (*rsa.PublicKey, error) {
	c.mu.RLock()
	if key, ok := c.keys[kid]; ok && time.Since(c.fetchedAt) < c.ttl {
		c.mu.RUnlock()
		return key, nil
	}
	c.mu.RUnlock()

	if err := c.refresh(); err != nil {
		return nil, err
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	key, ok := c.keys[kid]
	if !ok {
		return nil, fmt.Errorf("kid %q not found in JWKS", kid)
	}
	return key, nil
}

func (c *cachedJWKS) refresh() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check under write lock.
	if time.Since(c.fetchedAt) < c.ttl && len(c.keys) > 0 {
		return nil
	}

	resp, err := c.httpGet(c.certsURL)
	if err != nil {
		return fmt.Errorf("fetching JWKS: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("JWKS endpoint returned %d", resp.StatusCode)
	}

	var jwks jwksResponse
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return fmt.Errorf("decoding JWKS: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey, len(jwks.Keys))
	for _, k := range jwks.Keys {
		if k.Kty != "RSA" {
			continue
		}
		pub, err := parseRSAPublicKey(k.N, k.E)
		if err != nil {
			continue
		}
		keys[k.Kid] = pub
	}

	c.keys = keys
	c.fetchedAt = time.Now()
	return nil
}

func parseRSAPublicKey(nStr, eStr string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(nStr)
	if err != nil {
		return nil, err
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(eStr)
	if err != nil {
		return nil, err
	}
	n := new(big.Int).SetBytes(nBytes)
	e := new(big.Int).SetBytes(eBytes)
	return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
}

// CFAccessValidator validates Cloudflare Access JWTs.
type CFAccessValidator struct {
	teamDomain string
	audience   string
	keys       *cachedJWKS
}

// NewCFAccessValidator creates a validator for the given CF Access team domain and application AUD.
func NewCFAccessValidator(teamDomain, audience string) *CFAccessValidator {
	certsURL := fmt.Sprintf("https://%s/cdn-cgi/access/certs", teamDomain)
	return &CFAccessValidator{
		teamDomain: teamDomain,
		audience:   audience,
		keys:       newCachedJWKS(certsURL),
	}
}

// Validate parses and validates a CF Access JWT string.
func (v *CFAccessValidator) Validate(tokenString string) (*CFAccessClaims, error) {
	claims := &CFAccessClaims{}
	_, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		kid, ok := token.Header["kid"].(string)
		if !ok {
			return nil, fmt.Errorf("missing kid in token header")
		}
		return v.keys.getKey(kid)
	}, jwt.WithAudience(v.audience), jwt.WithExpirationRequired())
	if err != nil {
		return nil, err
	}
	return claims, nil
}

// Middleware returns an HTTP middleware that validates the Cf-Access-Jwt-Assertion header.
// Requests without the header (e.g. CLI with bearer token) are passed through
// to allow fallback auth.
func (v *CFAccessValidator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("Cf-Access-Jwt-Assertion")
		if token == "" {
			// No CF Access token — let downstream auth (bearer token) handle it.
			next.ServeHTTP(w, r)
			return
		}
		if _, err := v.Validate(token); err != nil {
			http.Error(w, "invalid cf access token", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
