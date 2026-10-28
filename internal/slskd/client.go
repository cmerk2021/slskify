package slskd

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Error is a human-readable slskd API error.
type Error struct {
	Status  int
	Message string
	Cause   error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Cause }

// ErrUnauthorized is returned when the API key is rejected.
var ErrUnauthorized = errors.New("slskd rejected the API key (check the key and that it is listed under web.authentication.api_keys in slskd.yml)")

// Client talks to slskd. It is safe for concurrent use.
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
	// PollInterval controls how often search status is polled.
	PollInterval time.Duration
	// MaxBusyRetries bounds retries when slskd answers 429/409 because
	// another search or enqueue request is in flight.
	MaxBusyRetries int

	sem   chan struct{}
	sleep func(ctx context.Context, d time.Duration) error
}

// NewClient creates a client. maxConcurrentSearches bounds the number of
// searches slskify has open on slskd at once; extra searches wait locally
// instead of failing.
func NewClient(baseURL, apiKey string, maxConcurrentSearches int) *Client {
	if maxConcurrentSearches <= 0 {
		maxConcurrentSearches = 2
	}
	return &Client{
		BaseURL:      strings.TrimRight(baseURL, "/"),
		APIKey:       apiKey,
		HTTP:         &http.Client{Timeout: 30 * time.Second},
		PollInterval: time.Second,
		sem:          make(chan struct{}, maxConcurrentSearches),
	}
}

func (c *Client) doSleep(ctx context.Context, d time.Duration) error {
	if c.sleep != nil {
		return c.sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) (int, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+"/api/v0"+path, rdr)
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-API-Key", c.APIKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, &Error{Message: fmt.Sprintf("could not reach slskd at %s", c.BaseURL), Cause: err}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return resp.StatusCode, &Error{Status: resp.StatusCode, Message: "reading slskd response failed", Cause: err}
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return resp.StatusCode, ErrUnauthorized
	case resp.StatusCode == http.StatusForbidden:
		return resp.StatusCode, &Error{Status: 403, Message: "slskd refused the request (API key lacks read-write role, or this instance is a relay agent)"}
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		if out != nil && len(bytes.TrimSpace(data)) > 0 {
			if err := json.Unmarshal(data, out); err != nil {
				return resp.StatusCode, fmt.Errorf("decoding slskd response from %s: %w", path, err)
			}
		}
		return resp.StatusCode, nil
	default:
		msg := strings.TrimSpace(string(data))
		if len(msg) > 200 {
			msg = msg[:200] + "…"
		}
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return resp.StatusCode, &Error{Status: resp.StatusCode, Message: fmt.Sprintf("slskd: %s", msg)}
	}
}

// isBusy reports whether slskd refused because of its own concurrency
// limits: 429 "Only one concurrent operation is permitted" on search/enqueue
// POSTs, or 409 when the search service rejects a new search.
func isBusy(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusConflict
}

// Ping verifies the URL and API key by fetching application state.
func (c *Client) Ping(ctx context.Context) error {
	var v map[string]any
	_, err := c.do(ctx, http.MethodGet, "/application", nil, &v)
	return err
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// searchTimeoutParam converts our timeout to slskd's searchTimeout field.
// slskd passes it straight to Soulseek.NET SearchOptions, whose
// searchTimeout is in milliseconds (default 15000).
func searchTimeoutParam(seconds int) int {
	if seconds <= 0 {
		seconds = 15
	}
	return seconds * 1000
}

type searchRequest struct {
	ID                       string `json:"id"`
	SearchText               string `json:"searchText"`
	SearchTimeout            int    `json:"searchTimeout"`
	ResponseLimit            *int   `json:"responseLimit,omitempty"`
	FileLimit                *int   `json:"fileLimit,omitempty"`
	FilterResponses          *bool  `json:"filterResponses,omitempty"`
	MinimumResponseFileCount *int   `json:"minimumResponseFileCount,omitempty"`
	MinimumPeerUploadSpeed   *int   `json:"minimumPeerUploadSpeed,omitempty"`
	MaximumPeerQueueLength   *int   `json:"maximumPeerQueueLength,omitempty"`
}

func optInt(v int) *int {
	if v <= 0 {
		return nil
	}
	return &v
}

// Search runs a complete search: create, poll until complete or timeout,
// fetch responses, and delete the search. Concurrency is bounded locally:
// when the limit is reached, callers wait rather than error.
func (c *Client) Search(ctx context.Context, text string, opts SearchOptions) ([]Response, error) {
	if c.sem != nil {
		select {
		case c.sem <- struct{}{}:
			defer func() { <-c.sem }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	req := searchRequest{
		ID:                       newID(),
		SearchText:               text,
		SearchTimeout:            searchTimeoutParam(opts.TimeoutSeconds),
		ResponseLimit:            optInt(opts.ResponseLimit),
		FileLimit:                optInt(opts.FileLimit),
		MinimumResponseFileCount: optInt(opts.MinimumResponseFileCount),
		MinimumPeerUploadSpeed:   optInt(opts.MinimumPeerUploadSpeed),
		MaximumPeerQueueLength:   optInt(opts.MaximumPeerQueueLength),
	}
	if req.MinimumResponseFileCount != nil || req.MinimumPeerUploadSpeed != nil || req.MaximumPeerQueueLength != nil {
		t := true
		req.FilterResponses = &t
	}
	var s Search
	if err := c.withBusyRetry(ctx, func() (int, error) {
		return c.do(ctx, http.MethodPost, "/searches", req, &s)
	}); err != nil {
		return nil, err
	}
	id := s.ID
	if id == "" {
		id = req.ID
	}
	defer c.deleteSearch(id)

	timeout := opts.TimeoutSeconds
	if timeout <= 0 {
		timeout = 15
	}
	deadline := time.Now().Add(time.Duration(timeout)*time.Second + 15*time.Second)
	poll := c.PollInterval
	if poll <= 0 {
		poll = time.Second
	}
	for !s.IsComplete {
		if time.Now().After(deadline) {
			// Ask slskd to stop; responses received so far are still usable.
			_, _ = c.do(ctx, http.MethodPut, "/searches/"+url.PathEscape(id), nil, nil)
			break
		}
		if err := c.doSleep(ctx, poll); err != nil {
			return nil, err
		}
		if _, err := c.do(ctx, http.MethodGet, "/searches/"+url.PathEscape(id), nil, &s); err != nil {
			return nil, err
		}
	}
	var responses []Response
	if _, err := c.do(ctx, http.MethodGet, "/searches/"+url.PathEscape(id)+"/responses", nil, &responses); err != nil {
		return nil, err
	}
	return responses, nil
}

func (c *Client) deleteSearch(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = c.do(ctx, http.MethodDelete, "/searches/"+url.PathEscape(id), nil, nil)
}

func (c *Client) withBusyRetry(ctx context.Context, f func() (int, error)) error {
	max := c.MaxBusyRetries
	if max == 0 {
		max = 20
	}
	wait := 250 * time.Millisecond
	for attempt := 0; ; attempt++ {
		status, err := f()
		if err == nil {
			return nil
		}
		if !isBusy(status) || attempt >= max {
			return err
		}
		if serr := c.doSleep(ctx, wait); serr != nil {
			return serr
		}
		if wait < 4*time.Second {
			wait *= 2
		}
	}
}

type enqueueFile struct {
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
}

// Enqueue asks slskd to download one file and returns its transfer ID when
// slskd reports it. Older slskd versions answer 201 with no body; then the
// returned ID is empty and callers locate the transfer by
// (username, filename) via Downloads.
func (c *Client) Enqueue(ctx context.Context, username, filename string, size int64) (string, error) {
	var out struct {
		Enqueued []Transfer        `json:"enqueued"`
		Failed   []json.RawMessage `json:"failed"`
	}
	body := []enqueueFile{{Filename: filename, Size: size}}
	err := c.withBusyRetry(ctx, func() (int, error) {
		return c.do(ctx, http.MethodPost, "/transfers/downloads/"+url.PathEscape(username), body, &out)
	})
	if err != nil {
		return "", err
	}
	if len(out.Enqueued) == 0 && len(out.Failed) > 0 {
		// Failed is a list of (filename, message) tuples; their JSON shape is
		// not stable across versions, so include it raw.
		return "", &Error{Message: "slskd could not enqueue the file: " + string(out.Failed[0])}
	}
	for _, t := range out.Enqueued {
		if t.Filename == filename {
			return t.ID, nil
		}
	}
	if len(out.Enqueued) > 0 {
		return out.Enqueued[0].ID, nil
	}
	return "", nil
}

// Downloads lists all current download transfers.
func (c *Client) Downloads(ctx context.Context) ([]Transfer, error) {
	var users []transferUser
	if _, err := c.do(ctx, http.MethodGet, "/transfers/downloads", nil, &users); err != nil {
		return nil, err
	}
	var out []Transfer
	for _, u := range users {
		for _, d := range u.Directories {
			for _, f := range d.Files {
				if f.Username == "" {
					f.Username = u.Username
				}
				out = append(out, f)
			}
		}
	}
	return out, nil
}

// CancelDownload cancels a transfer. With remove, the record is also
// removed from slskd's list (best-effort: slskd only removes transfers in a
// terminal state, so removal is retried briefly after cancelling).
func (c *Client) CancelDownload(ctx context.Context, username, id string, remove bool) error {
	path := "/transfers/downloads/" + url.PathEscape(username) + "/" + url.PathEscape(id)
	status, err := c.do(ctx, http.MethodDelete, path, nil, nil)
	if err != nil && status != http.StatusNotFound {
		return err
	}
	if !remove || status == http.StatusNotFound {
		return nil
	}
	for i := 0; i < 3; i++ {
		if serr := c.doSleep(ctx, 500*time.Millisecond); serr != nil {
			return serr
		}
		st, rerr := c.do(ctx, http.MethodDelete, path+"?remove=true", nil, nil)
		if rerr == nil || st == http.StatusNotFound {
			return nil
		}
	}
	return nil
}
