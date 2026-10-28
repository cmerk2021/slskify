package slskd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMapState(t *testing.T) {
	cases := []struct {
		raw    string
		state  State
		reason string
	}{
		{"None", StateQueued, ""},
		{"Requested", StateQueued, ""},
		{"Queued, Locally", StateQueued, ""},
		{"Queued, Remotely", StateQueued, ""},
		{"Initializing", StateInitializing, ""},
		{"InProgress", StateInProgress, ""},
		{"Completed, Succeeded", StateSucceeded, ""},
		{"Completed, Rejected", StateFailed, "rejected"},
		{"Completed, Errored", StateFailed, "errored"},
		{"Completed, TimedOut", StateFailed, "timed out"},
		{"Completed, Cancelled", StateFailed, "cancelled"},
		{"Completed, Aborted", StateFailed, "aborted"},
		{"completed,succeeded", StateSucceeded, ""},
	}
	for _, c := range cases {
		s, r := MapState(StateFlags(c.raw))
		if s != c.state || r != c.reason {
			t.Errorf("MapState(%q) = %v,%q want %v,%q", c.raw, s, r, c.state, c.reason)
		}
	}
	var f StateFlags
	if err := json.Unmarshal([]byte(`48`), &f); err != nil || f != "Completed, Succeeded" {
		t.Errorf("numeric state decode: %q %v", f, err)
	}
}

type fakeSlskd struct {
	mu          sync.Mutex
	open        int
	maxOpen     int
	posts       int
	busyFirst   int
	deleted     []string
	pollsNeeded int
	polls       map[string]int
	lastPost    searchRequest
}

func (f *fakeSlskd) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "key" {
			w.WriteHeader(401)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		p := r.URL.Path
		switch {
		case r.Method == http.MethodPost && p == "/api/v0/searches":
			f.posts++
			if f.busyFirst > 0 {
				f.busyFirst--
				w.WriteHeader(429)
				_, _ = w.Write([]byte("Only one concurrent operation is permitted."))
				return
			}
			var req searchRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			f.lastPost = req
			f.open++
			if f.open > f.maxOpen {
				f.maxOpen = f.open
			}
			_ = json.NewEncoder(w).Encode(Search{ID: req.ID, SearchText: req.SearchText, State: "InProgress"})
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/responses"):
			_, _ = w.Write([]byte(`[{"username":"u1","fileCount":1,"hasFreeUploadSlot":true,"queueLength":0,"uploadSpeed":1000,
				"files":[{"filename":"@@x\\Music\\A - B.mp3","size":5000000,"extension":"mp3","bitRate":320,"length":200,"isVariableBitRate":false,"isLocked":false}],
				"lockedFiles":[{"filename":"@@x\\L.flac","size":1,"isLocked":true}]}]`))
		case r.Method == http.MethodGet && strings.HasPrefix(p, "/api/v0/searches/"):
			id := strings.TrimPrefix(p, "/api/v0/searches/")
			f.polls[id]++
			done := f.polls[id] >= f.pollsNeeded
			st := "InProgress"
			if done {
				st = "Completed, TimedOut"
			}
			_ = json.NewEncoder(w).Encode(Search{ID: id, State: st, IsComplete: done})
		case r.Method == http.MethodDelete && strings.HasPrefix(p, "/api/v0/searches/"):
			f.open--
			f.deleted = append(f.deleted, strings.TrimPrefix(p, "/api/v0/searches/"))
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected %s %s", r.Method, p)
			w.WriteHeader(404)
		}
	})
}

func newTest(srvURL string, max int) *Client {
	c := NewClient(srvURL, "key", max)
	c.PollInterval = time.Millisecond
	return c
}

func TestSearchLifecycle(t *testing.T) {
	f := &fakeSlskd{pollsNeeded: 3, polls: map[string]int{}, busyFirst: 2}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	c := newTest(srv.URL, 2)
	c.sleep = func(context.Context, time.Duration) error { return nil }
	res, err := c.Search(context.Background(), "artist title", SearchOptions{TimeoutSeconds: 10, FileLimit: 500, MinimumPeerUploadSpeed: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Files[0].BitRate == nil || *res[0].Files[0].BitRate != 320 || len(res[0].LockedFiles) != 1 {
		t.Fatalf("bad responses %+v", res)
	}
	if f.posts != 3 || len(f.deleted) != 1 || f.open != 0 {
		t.Fatalf("posts=%d deleted=%v open=%d", f.posts, f.deleted, f.open)
	}
	if f.lastPost.SearchTimeout != 10000 || f.lastPost.FileLimit == nil || *f.lastPost.FileLimit != 500 || f.lastPost.FilterResponses == nil {
		t.Fatalf("bad request %+v", f.lastPost)
	}
}

func TestSearchConcurrencyQueuesLocally(t *testing.T) {
	f := &fakeSlskd{pollsNeeded: 5, polls: map[string]int{}}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	c := newTest(srv.URL, 2)
	var wg sync.WaitGroup
	var errs atomic.Int32
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Search(context.Background(), "q", SearchOptions{}); err != nil {
				errs.Add(1)
			}
		}()
	}
	wg.Wait()
	if errs.Load() != 0 || f.maxOpen > 2 || len(f.deleted) != 6 {
		t.Fatalf("errs=%d maxOpen=%d deleted=%d", errs.Load(), f.maxOpen, len(f.deleted))
	}
}

func TestUnauthorized(t *testing.T) {
	srv := httptest.NewServer((&fakeSlskd{}).handler(t))
	defer srv.Close()
	c := NewClient(srv.URL, "wrong", 1)
	if err := c.Ping(context.Background()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("got %v", err)
	}
}

func TestEnqueueDownloadsAndCancel(t *testing.T) {
	var deletes []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v0/transfers/downloads/user one":
			var files []enqueueFile
			_ = json.NewDecoder(r.Body).Decode(&files)
			if len(files) != 1 || files[0].Size != 123 {
				w.WriteHeader(400)
				return
			}
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(map[string]any{"enqueued": []Transfer{{ID: "abc", Username: "user one", Filename: files[0].Filename}}, "failed": []any{}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v0/transfers/downloads/old":
			w.WriteHeader(201)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v0/transfers/downloads/offline":
			w.WriteHeader(500)
			_, _ = w.Write([]byte("User offline appears to be offline"))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v0/transfers/downloads":
			_, _ = w.Write([]byte(`[{"username":"u","directories":[{"directory":"d","files":[
				{"id":"1","username":"u","filename":"d\\a.mp3","size":10,"state":"Completed, Succeeded","bytesTransferred":10,"averageSpeed":5.5},
				{"id":"2","filename":"d\\b.mp3","size":10,"state":"InProgress","bytesTransferred":4,"percentComplete":40}]}]}]`))
		case r.Method == http.MethodDelete:
			deletes = append(deletes, r.URL.RequestURI())
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := newTest(srv.URL, 1)
	c.sleep = func(context.Context, time.Duration) error { return nil }
	ctx := context.Background()
	id, err := c.Enqueue(ctx, "user one", `@@x\a.mp3`, 123)
	if err != nil || id != "abc" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if id, err := c.Enqueue(ctx, "old", "f", 123); err != nil || id != "" {
		t.Fatalf("legacy enqueue: %q %v", id, err)
	}
	if _, err := c.Enqueue(ctx, "offline", "f", 123); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("expected offline error, got %v", err)
	}
	ts, err := c.Downloads(ctx)
	if err != nil || len(ts) != 2 || ts[1].Username != "u" {
		t.Fatalf("downloads: %+v %v", ts, err)
	}
	if s, _ := ts[0].Status(); s != StateSucceeded {
		t.Fatalf("state %v", s)
	}
	if s, _ := ts[1].Status(); s != StateInProgress || ts[1].BytesTransferred != 4 {
		t.Fatalf("state %v", s)
	}
	if err := c.CancelDownload(ctx, "u", "2", true); err != nil {
		t.Fatal(err)
	}
	if len(deletes) != 2 || !strings.HasSuffix(deletes[1], "?remove=true") {
		t.Fatalf("deletes=%v", deletes)
	}
}
