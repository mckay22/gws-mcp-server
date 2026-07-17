package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/mckay22/gws-mcp-server/internal/config"
	"github.com/mckay22/gws-mcp-server/internal/gapi"
	"github.com/mckay22/gws-mcp-server/internal/googleauth"
	"github.com/mckay22/gws-mcp-server/internal/oidcauth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// httpFakeIssuer is a compact OIDC issuer for the resource-server integration
// test: discovery, JWKS, and JWT signing.
type httpFakeIssuer struct {
	url    string
	signer jose.Signer
	pub    *rsa.PublicKey
	kid    string
}

func newHTTPFakeIssuer(t *testing.T) *httpFakeIssuer {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	fi := &httpFakeIssuer{pub: &priv.PublicKey, kid: "itest-key"}
	fi.signer, err = jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: priv},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", fi.kid),
	)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                fi.url,
			"jwks_uri":                              fi.url + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key: fi.pub, KeyID: fi.kid, Algorithm: "RS256", Use: "sig",
		}}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	fi.url = srv.URL
	return fi
}

func (fi *httpFakeIssuer) token(t *testing.T, aud, email string) string {
	t.Helper()
	now := time.Now()
	payload, _ := json.Marshal(map[string]any{
		"iss": fi.url, "aud": aud, "sub": "sub-1", "email": email,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	})
	jws, err := fi.signer.Sign(payload)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	tok, _ := jws.CompactSerialize()
	return tok
}

// writeFakeSAKey writes a syntactically valid service-account JSON key (a real
// RSA private key) so NewDWD parses successfully. No token is ever minted in this
// test (health makes no Google call), so token_uri is never contacted.
func writeFakeSAKey(t *testing.T) string {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("sa key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	keyJSON, _ := json.Marshal(map[string]string{
		"type":           "service_account",
		"project_id":     "test-project",
		"private_key_id": "sa-key-1",
		"private_key":    string(pemKey),
		"client_email":   "dwd-sa@test-project.iam.gserviceaccount.com",
		"client_id":      "123",
		"token_uri":      "https://oauth2.googleapis.com/token",
	})
	path := filepath.Join(t.TempDir(), "sa.json")
	if err := os.WriteFile(path, keyJSON, 0o600); err != nil {
		t.Fatalf("write sa key: %v", err)
	}
	return path
}

// bearerRoundTripper injects an Authorization header on every request.
type bearerRoundTripper struct {
	token string
	base  http.RoundTripper
}

func (b bearerRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	if b.token != "" {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return b.base.RoundTrip(r)
}

// newResourceServer builds the resource-server HTTP handler wired to a fake
// issuer and a fake SA key, and returns its test server plus the audience.
func newResourceServer(t *testing.T, fi *httpFakeIssuer) (*httptest.Server, config.Config) {
	t.Helper()
	cfg := config.Config{
		Audience:       "api://gws-itest",
		AllowedIssuers: []string{fi.url},
		DWDKeyPath:     writeFakeSAKey(t),
	}
	verifier, err := oidcauth.NewVerifier(context.Background(), cfg.Issuers(), cfg.Audience, cfg.SubjectClaimOrDefault())
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	dwd, err := googleauth.NewDWD(cfg.DWDKeyPath, requiredScopes(cfg))
	if err != nil {
		t.Fatalf("dwd: %v", err)
	}
	srv := httptest.NewServer(mcpHTTPHandler(cfg, verifier, gapi.New(dwd), nil))
	t.Cleanup(srv.Close)
	return srv, cfg
}

func TestHTTPRejectsUnauthenticated(t *testing.T) {
	fi := newHTTPFakeIssuer(t)
	srv, _ := newResourceServer(t, fi)

	// A POST to /mcp with no bearer token must be rejected (401).
	resp, err := http.Post(srv.URL+"/mcp", "application/json", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for an unauthenticated /mcp request", resp.StatusCode)
	}
}

func TestHTTPServesResourceMetadata(t *testing.T) {
	fi := newHTTPFakeIssuer(t)
	srv, cfg := newResourceServer(t, fi)

	resp, err := http.Get(srv.URL + "/.well-known/oauth-protected-resource")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	var doc map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc["resource"] != cfg.Audience {
		t.Errorf("resource = %v, want %v", doc["resource"], cfg.Audience)
	}
}

func TestHTTPHealthWithValidToken(t *testing.T) {
	fi := newHTTPFakeIssuer(t)
	srv, cfg := newResourceServer(t, fi)

	token := fi.token(t, cfg.Audience, "ada@example.com")
	httpClient := &http.Client{Transport: bearerRoundTripper{token: token, base: http.DefaultTransport}}
	transport := &mcp.StreamableClientTransport{
		Endpoint:   srv.URL + "/mcp",
		HTTPClient: httpClient,
	}

	ctx := context.Background()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "itest", Version: "t"}, nil).Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer cs.Close()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "health", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("call health: %v", err)
	}
	if res.IsError {
		t.Fatalf("health tool error: %v", res.Content)
	}
	b, _ := json.Marshal(res.StructuredContent)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	if out["transport"] != "http" {
		t.Errorf("transport = %v, want http", out["transport"])
	}
	if out["mode"] != config.ModeResourceServer {
		t.Errorf("mode = %v, want %s", out["mode"], config.ModeResourceServer)
	}
}

// TestHTTPBadTokenRejected confirms a token from an untrusted issuer/audience is
// rejected by the bearer verifier.
func TestHTTPBadTokenRejected(t *testing.T) {
	fi := newHTTPFakeIssuer(t)
	srv, _ := newResourceServer(t, fi)

	// Wrong audience → the verifier rejects it.
	bad := fi.token(t, "api://not-this-server", "ada@example.com")
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+bad)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for wrong-audience token", resp.StatusCode)
	}
}
