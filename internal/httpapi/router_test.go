package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jfortner8/archive-api/internal/domain"
	"github.com/jfortner8/archive-api/internal/domain/itemtypes"
	"github.com/jfortner8/archive-api/internal/httpapi/gen"
)

const (
	testAPIKey    = "test-key"
	ownerToken    = "owner-token"
	viewerToken   = "viewer-token"
	strangerToken = "stranger-token"
)

var (
	ownerAccount    = domain.AccountID("acct-owner")
	viewerAccount   = domain.AccountID("acct-viewer")
	strangerAccount = domain.AccountID("acct-stranger")

	testArchive  = domain.ArchiveID("arc_test")
	otherArchive = domain.ArchiveID("arc_other")
)

func newTestServer(t *testing.T) (*Server, *fakeStore) {
	t.Helper()

	data := newFakeStore()
	data.seedArchive(testArchive, "Test Archive", ownerAccount, domain.RoleOwner)
	_ = data.PutMember(nil, domain.Member{
		ArchiveID: testArchive, AccountID: viewerAccount, Role: domain.RoleViewer,
	})
	data.seedArchive(otherArchive, "Someone Else", strangerAccount, domain.RoleOwner)

	return &Server{
		Store: data,
		Files: fakePresigner{},
		Verifier: fakeVerifier{
			ownerToken:    ownerAccount,
			viewerToken:   viewerAccount,
			strangerToken: strangerAccount,
		},
		Types:  itemtypes.Default,
		APIKey: testAPIKey,
	}, data
}

type requestOpts struct {
	token   string
	body    any
	ifMatch string
	noKey   bool
}

func do(t *testing.T, s *Server, method, path string, opts requestOpts) *httptest.ResponseRecorder {
	t.Helper()

	var body io.Reader
	if opts.body != nil {
		raw, err := json.Marshal(opts.body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		body = bytes.NewReader(raw)
	}

	req := httptest.NewRequest(method, path, body)
	if opts.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if !opts.noKey {
		req.Header.Set("X-API-Key", testAPIKey)
	}
	if opts.token != "" {
		req.Header.Set("X-User-Token", opts.token)
	}
	if opts.ifMatch != "" {
		req.Header.Set("If-Match", opts.ifMatch)
	}

	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding %s: %v", rec.Body.String(), err)
	}
	return out
}

// TestEveryRouteRequiresAuth walks the route table rather than listing routes
// by hand, so a route added later is covered automatically.
//
// This is the test that makes the middleware chain a guarantee instead of a
// convention. The previous design applied auth per registration, so a new
// route shipped unprotected unless someone remembered - and nothing would
// have noticed.
func TestEveryRouteRequiresAuth(t *testing.T) {
	s, _ := newTestServer(t)

	for _, rt := range s.v1Routes() {
		path := concretePath(rt.Pattern)

		t.Run(rt.Method+" "+rt.Pattern, func(t *testing.T) {
			// No API key at all.
			if rec := do(t, s, rt.Method, path, requestOpts{noKey: true, token: ownerToken}); rec.Code != http.StatusUnauthorized {
				t.Errorf("without an API key: status = %d, want 401", rec.Code)
			}
			// Key but no token.
			if rec := do(t, s, rt.Method, path, requestOpts{}); rec.Code != http.StatusUnauthorized {
				t.Errorf("without a token: status = %d, want 401", rec.Code)
			}
			// Key plus a token that does not verify.
			if rec := do(t, s, rt.Method, path, requestOpts{token: "nonsense"}); rec.Code != http.StatusUnauthorized {
				t.Errorf("with an invalid token: status = %d, want 401", rec.Code)
			}
		})
	}
}

// TestArchiveScopedRoutesHideOtherArchives covers the disclosure rule: a
// non-member is told "not found", never "forbidden", so archive ids cannot be
// probed by watching which error comes back.
func TestArchiveScopedRoutesHideOtherArchives(t *testing.T) {
	s, _ := newTestServer(t)

	for _, rt := range s.v1Routes() {
		if !rt.RequiresArchiveMembership {
			continue
		}
		path := concretePath(rt.Pattern)

		t.Run(rt.Method+" "+rt.Pattern, func(t *testing.T) {
			rec := do(t, s, rt.Method, path, requestOpts{token: strangerToken, body: map[string]any{}})
			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404 for a non-member", rec.Code)
			}

			body := decode[gen.Error](t, rec)
			if body.Error.Code != gen.ErrorCodeNotFound {
				t.Errorf("code = %q, want not_found", body.Error.Code)
			}
		})
	}
}

// TestViewersCannotWrite covers the safe-by-default role check: anything that
// is not a GET needs write access, so a mutating route added later is
// protected without anyone having to remember.
func TestViewersCannotWrite(t *testing.T) {
	s, _ := newTestServer(t)

	for _, rt := range s.v1Routes() {
		if !rt.RequiresArchiveMembership || rt.Method == http.MethodGet {
			continue
		}

		t.Run(rt.Method+" "+rt.Pattern, func(t *testing.T) {
			rec := do(t, s, rt.Method, concretePath(rt.Pattern), requestOpts{
				token: viewerToken, body: map[string]any{},
			})
			if rec.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403 for a viewer", rec.Code)
			}
		})
	}
}

// concretePath fills a route pattern with real ids.
func concretePath(pattern string) string {
	path := strings.ReplaceAll(pattern, "{archiveId}", string(testArchive))
	path = strings.ReplaceAll(path, "{itemId}", "itm_missing")
	path = strings.ReplaceAll(path, "{accountId}", "acct-someone")
	return strings.ReplaceAll(path, "{fileId}", "fil_missing")
}

func TestHealthzNeedsNothing(t *testing.T) {
	s, _ := newTestServer(t)

	rec := do(t, s, "GET", "/healthz", requestOpts{noKey: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// TestUnknownPathsAndMethodsReturnJSON covers what the old API leaked:
// ServeMux answers an unmatched path with plaintext "404 page not found" and
// a wrong method with a plaintext 405, while every real error was JSON.
func TestUnknownPathsAndMethodsReturnJSON(t *testing.T) {
	s, _ := newTestServer(t)

	t.Run("unknown path", func(t *testing.T) {
		rec := do(t, s, "GET", "/nope", requestOpts{})
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type = %q, want application/json", ct)
		}
		if body := decode[gen.Error](t, rec); body.Error.Code != gen.ErrorCodeNotFound {
			t.Errorf("code = %q, want not_found", body.Error.Code)
		}
	})

	t.Run("wrong method", func(t *testing.T) {
		rec := do(t, s, "DELETE", "/healthz", requestOpts{})
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want 405", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type = %q, want application/json", ct)
		}
		if body := rec.Body.String(); !strings.Contains(body, `"error"`) {
			t.Errorf("body = %q, want the JSON error envelope", body)
		}
	})
}

func TestRequestIDIsEchoed(t *testing.T) {
	s, _ := newTestServer(t)

	rec := do(t, s, "GET", "/healthz", requestOpts{})
	if rec.Header().Get("X-Request-Id") == "" {
		t.Error("no X-Request-Id on the response")
	}

	// And it reaches the error envelope, so a user reporting a failure can
	// be matched to its log line.
	rec = do(t, s, "GET", "/v1/me", requestOpts{})
	body := decode[gen.Error](t, rec)
	if body.Error.RequestId == nil || *body.Error.RequestId == "" {
		t.Error("no requestId in the error envelope")
	}
}

// TestSpecCoversEveryRoute is the drift check that does not depend on
// codegen: every route the server registers has to appear in openapi.yaml,
// and vice versa.
//
// Generated models catch a changed *shape*; this catches a route added,
// removed or renamed without the spec being touched - which is the way a
// spec most often becomes a lie.
func TestSpecCoversEveryRoute(t *testing.T) {
	raw, err := os.ReadFile("../../openapi.yaml")
	if err != nil {
		t.Fatalf("reading the spec: %v", err)
	}
	spec := string(raw)

	s, _ := newTestServer(t)

	inSpec := map[string]bool{}
	for _, line := range strings.Split(spec, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "/") && strings.HasSuffix(trimmed, ":") {
			inSpec[strings.TrimSuffix(trimmed, ":")] = true
		}
	}

	registered := map[string]bool{"/healthz": true}
	for _, rt := range s.v1Routes() {
		registered[rt.Pattern] = true

		if !inSpec[rt.Pattern] {
			t.Errorf("route %s %s is not in openapi.yaml", rt.Method, rt.Pattern)
		}

		// The method has to be documented too, not just the path.
		if !specHasOperation(spec, rt.Pattern, rt.Method) {
			t.Errorf("openapi.yaml documents %s but not its %s operation", rt.Pattern, rt.Method)
		}
	}

	for path := range inSpec {
		if !registered[path] {
			t.Errorf("openapi.yaml documents %s, which the server does not serve", path)
		}
	}
}

// specHasOperation looks for a method under a path, by walking from the path
// line to the next path line.
func specHasOperation(spec, path, method string) bool {
	lines := strings.Split(spec, "\n")

	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == path+":" {
			start = i
			break
		}
	}
	if start < 0 {
		return false
	}

	want := strings.ToLower(method) + ":"
	for i := start + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "/") && strings.HasSuffix(trimmed, ":") {
			return false // reached the next path
		}
		if trimmed == want {
			return true
		}
	}
	return false
}

func TestGeneratedModelsAreCurrent(t *testing.T) {
	// A cheap sanity check that `make generate` was run: the generated file
	// has to mention a schema only the current spec defines.
	raw, err := os.ReadFile("gen/models.gen.go")
	if err != nil {
		t.Fatalf("reading generated models: %v", err)
	}
	for _, want := range []string{"type ItemCard struct", "type ArchiveDate struct", "CoverPinned"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("generated models are missing %q - run `make generate`", want)
		}
	}
}

func mustCreateItem(t *testing.T, s *Server, data *fakeStore) domain.Item {
	t.Helper()

	rec := do(t, s, "POST", fmt.Sprintf("/v1/archives/%s/items", testArchive), requestOpts{
		token: ownerToken,
		body: map[string]any{
			"typeId": "photo",
			"title":  "Blue Ridge",
			"date":   map[string]any{"kind": "exact", "date": "1952-06-15"},
		},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	created := decode[gen.Item](t, rec)
	item, err := data.GetItem(nil, testArchive, domain.ItemID(created.Id))
	if err != nil {
		t.Fatalf("created item not in store: %v", err)
	}
	return item
}

// TestMain silences the request log. These tests make hundreds of requests
// and the log lines bury the failures.
func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}
