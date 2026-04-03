package cache

import (
	"bytes"
	"io"
	"net/http"
	"sync"
)

type entry struct {
	etag   string
	body   []byte
	header http.Header
}

// Transport is an http.RoundTripper that caches responses using ETags.
// On cache hit (304 Not Modified), it replays the cached response body,
// which avoids consuming GitHub API rate limit.
type Transport struct {
	Base    http.RoundTripper
	mu      sync.Mutex
	entries map[string]*entry
}

func (t *Transport) base() http.RoundTripper {
	if t.Base != nil {
		return t.Base
	}

	return http.DefaultTransport
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	key := req.Method + " " + req.URL.String()

	t.mu.Lock()
	if t.entries == nil {
		t.entries = make(map[string]*entry)
	}

	if e, ok := t.entries[key]; ok {
		req = req.Clone(req.Context())
		req.Header.Set("If-None-Match", e.etag)
	}
	t.mu.Unlock()

	resp, err := t.base().RoundTrip(req)
	if err != nil {
		return resp, err
	}

	if resp.StatusCode == http.StatusNotModified {
		t.mu.Lock()
		e := t.entries[key]
		t.mu.Unlock()

		if e != nil {
			_ = resp.Body.Close()
			resp.StatusCode = http.StatusOK

			resp.Body = io.NopCloser(bytes.NewReader(e.body))
			for k, v := range e.header {
				resp.Header[k] = v
			}
		}

		return resp, nil
	}

	if etag := resp.Header.Get("ETag"); etag != "" {
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if readErr != nil {
			return nil, readErr
		}

		resp.Body = io.NopCloser(bytes.NewReader(body))

		t.mu.Lock()
		t.entries[key] = &entry{
			etag:   etag,
			body:   body,
			header: resp.Header.Clone(),
		}
		t.mu.Unlock()
	}

	return resp, nil
}
