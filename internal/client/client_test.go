package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func static(t *testing.T, token string) Auth {
	t.Helper()
	auth, err := Static(token)
	if err != nil {
		t.Fatal(err)
	}
	return auth
}

func testClient(t *testing.T, server *httptest.Server, token string) *Client {
	t.Helper()
	c, err := New(Config{Server: server.URL, Auth: static(t, token), WorkspaceID: "ws-1", UserAgent: "test/1"})
	if err != nil {
		t.Fatal(err)
	}
	c.retrying.RetryWaitMin, c.retrying.RetryWaitMax = 0, 0
	return c
}

func TestNewRefusesWhatCannotReachAServer(t *testing.T) {
	for name, cfg := range map[string]Config{
		"no scheme":            {Server: "api.example.test", Auth: static(t, "sbk_ak_x")},
		"no token":             {Server: "https://api.example.test"},
		"user without a space": {Server: "https://api.example.test", Auth: static(t, "sbk_at_x")},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: New succeeded", name)
		}
	}
	if _, err := New(Config{Server: "https://api.example.test/", Auth: static(t, "sbk_ak_x")}); err != nil {
		t.Errorf("an API key needs no workspace: %v", err)
	}
}

func TestEveryRequestCarriesTheWorkspaceAndEveryPostAKey(t *testing.T) {
	var seen []http.Header
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Clone())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/agents":
			_, _ = io.WriteString(w, `{"id":"a1","workspace_id":"ws-1","name":"n"}`)
		default:
			_, _ = io.WriteString(w, `{"id":"a1","workspace_id":"ws-1","name":"n"}`)
		}
	}))
	defer server.Close()
	c := testClient(t, server, "sbk_at_token")

	if _, err := c.CreateAgent(context.Background(), "n"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetAgent(context.Background(), "a1"); err != nil {
		t.Fatal(err)
	}
	create, get := seen[0], seen[1]
	if create.Get("Authorization") != "Bearer sbk_at_token" || create.Get("User-Agent") != "test/1" {
		t.Fatalf("create headers: %v", create)
	}
	if create.Get("Kikuvi-Workspace") != "ws-1" || create.Get("Idempotency-Key") == "" {
		t.Fatalf("a create names the workspace and is keyed: %v", create)
	}
	// A by-id route ignores the header, so carrying it needs no per-call
	// decision; only a POST is keyed.
	if get.Get("Kikuvi-Workspace") != "ws-1" {
		t.Fatalf("a by-id read names the workspace too: %v", get)
	}
	if get.Get("Idempotency-Key") != "" {
		t.Fatalf("a read is not keyed: %v", get)
	}
}

func TestAClientWithoutAWorkspaceSendsNoWorkspaceHeader(t *testing.T) {
	var seen http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		_, _ = io.WriteString(w, `{"id":"a1"}`)
	}))
	defer server.Close()
	c, err := New(Config{Server: server.URL, Auth: static(t, "sbk_ak_key")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateAgent(context.Background(), "n"); err != nil {
		t.Fatal(err)
	}
	if _, ok := seen["Kikuvi-Workspace"]; ok {
		t.Fatalf("headers = %v", seen)
	}
}

func TestARetriedPostReusesOneIdempotencyKeyAndResendsItsBody(t *testing.T) {
	var keys, bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		if len(keys) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"v1"}`)
	}))
	defer server.Close()
	c := testClient(t, server, "sbk_ak_key")

	id, err := c.CreateVault(context.Background(), "v", map[string]string{"team": "support"})
	if err != nil || id != "v1" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Fatalf("keys = %v", keys)
	}
	if bodies[0] == "" || bodies[0] != bodies[1] {
		t.Fatalf("bodies = %q", bodies)
	}
}

func TestAPostIsGivenUpOnAfterThreeAttempts(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"code":"unavailable","message":"try later"}}`)
	}))
	defer server.Close()
	c := testClient(t, server, "sbk_ak_key")

	_, err := c.CreateAgent(context.Background(), "n")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "unavailable" {
		t.Fatalf("err = %v", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestACancelledCallStopsRetrying(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	c := testClient(t, server, "sbk_ak_key")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.CreateAgent(ctx, "n"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if calls > 1 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestAPatchIsNotRetried(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	c := testClient(t, server, "sbk_ak_key")
	if _, err := c.RenameAgent(context.Background(), "a", "b"); err == nil {
		t.Fatal("a 503 succeeded")
	}
	if calls != 1 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestTheErrorEnvelopeIsDecoded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"agent not found"}}`)
	}))
	defer server.Close()
	c := testClient(t, server, "sbk_ak_key")
	_, err := c.GetAgent(context.Background(), "a")
	if !IsNotFound(err) {
		t.Fatalf("err = %v", err)
	}
	if err.Error() != "subako: not_found (HTTP 404): agent not found" {
		t.Fatalf("message = %q", err.Error())
	}
}

func TestListingsWalkEveryPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("after") {
		case "":
			_, _ = io.WriteString(w, `{"items":[{"id":"c1"}],"next_cursor":"c1"}`)
		case "c1":
			_, _ = io.WriteString(w, `{"items":[{"id":"c2","target":"https://t"}],"next_cursor":null}`)
		}
	}))
	defer server.Close()
	c := testClient(t, server, "sbk_ak_key")
	found, err := c.FindCredential(context.Background(), "v", "c2")
	if err != nil || found.Target != "https://t" {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if _, err := c.FindCredential(context.Background(), "v", "c3"); !IsNotFound(err) {
		t.Fatalf("a missing credential: %v", err)
	}
}
