package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"protobuf/internal/config"
	"protobuf/internal/platform"
	"protobuf/internal/testutil"
	"strings"
	"testing"
	"time"
)

func testServer(t *testing.T) (*Server, *testutil.Harness) {
	h := testutil.New(t)
	c := config.Config{AgentToken: "test"}
	return &Server{Core: h.Core, Redis: h.Redis, Config: c, Platforms: platform.New(c, h.Core, h.Redis)}, h
}
func request(t *testing.T, handler http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}
func TestAuthenticationAccessAndInputBoundaries(t *testing.T) {
	s, h := testServer(t)
	router := s.Router()
	u, token, w := h.User(t, "alice")
	_, other, _ := h.User(t, "bob")
	if got := request(t, router, "GET", "/v1/tasks?workspace_id="+w.ID, "", "").Code; got != 401 {
		t.Fatal(got)
	}
	if got := request(t, router, "GET", "/v1/tasks?workspace_id="+w.ID, "", other).Code; got != 403 {
		t.Fatal(got)
	}
	body := fmt.Sprintf(`{"workspace_id":%q,"title":"x","user_id":%q}`, w.ID, u.ID)
	if got := request(t, router, "POST", "/v1/tasks", body, token).Code; got != 400 {
		t.Fatal(got)
	}
	body = fmt.Sprintf(`{"workspace_id":%q,"title":"x"}`, w.ID)
	created := request(t, router, "POST", "/v1/tasks", body, token)
	if created.Code != 201 {
		t.Fatal(created.Body.String())
	}
	invalid := request(t, router, "GET", "/v1/agent-jobs/not-a-uuid", "", token)
	if invalid.Code != 400 {
		t.Fatal(invalid.Code)
	}
	csrf := httptest.NewRequest("POST", "/v1/workspaces", strings.NewReader(`{"name":"malicious"}`))
	csrf.Header.Set("Origin", "https://other.invalid")
	csrf.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, csrf)
	if rec.Code != 403 {
		t.Fatal(rec.Code)
	}
	login := request(t, router, "POST", "/v1/auth/login", `{"email":"alice@example.test","password":"test-password-123"}`, "")
	if login.Code != 200 {
		t.Fatal(login.Body.String())
	}
	if len(login.Result().Cookies()) != 1 || !login.Result().Cookies()[0].HttpOnly {
		t.Fatal("session cookie not HttpOnly")
	}
	if strings.Contains(login.Body.String(), "password_hash") {
		t.Fatal("password hash leaked")
	}
	if got := request(t, router, "GET", "/", "", "").Code; got != 200 {
		t.Fatal(got)
	}
}
func TestRealtimeNotificationIsUserScoped(t *testing.T) {
	s, h := testServer(t)
	u, token, w := h.User(t, "alice")
	other, _, otherSpace := h.User(t, "bob")
	server := httptest.NewServer(s.Router())
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/v1/stream", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	h.Core.Notify(ctx, other.ID, otherSpace.ID, "test", "secret from bob", "web", nil, 0)
	if err = h.Core.Notify(ctx, u.ID, w.ID, "test", "hello alice", "web", nil, 0); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			var n struct {
				Body string `json:"body"`
			}
			if err = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &n); err != nil {
				t.Fatal(err)
			}
			if n.Body != "hello alice" {
				t.Fatalf("other user's notification leaked: %s", n.Body)
			}
			return
		}
	}
	t.Fatalf("no notification: %v", scanner.Err())
}
