package cache

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func get(t *testing.T, client *http.Client, url string) *http.Response {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)

	return resp
}

func TestTransport_CachesWithETag(t *testing.T) {
	var hits atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"abc123"` {
			hits.Add(1)
			w.WriteHeader(http.StatusNotModified)

			return
		}

		w.Header().Set("ETag", `"abc123"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":1}]`))
	}))
	defer srv.Close()

	transport := &Transport{Base: http.DefaultTransport}
	client := &http.Client{Transport: transport}

	// First request — populates cache
	resp := get(t, client, srv.URL+"/repos/org/repo/issues")
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, `[{"id":1}]`, string(body))
	assert.Equal(t, int32(0), hits.Load())

	// Second request — should use ETag, get 304, return cached body
	resp = get(t, client, srv.URL+"/repos/org/repo/issues")
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, `[{"id":1}]`, string(body))
	assert.Equal(t, int32(1), hits.Load())
}

func TestTransport_UpdatesCacheOnNewETag(t *testing.T) {
	var callCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		w.Header().Set("ETag", fmt.Sprintf(`"v%d"`, n))
		w.Header().Set("Content-Type", "application/json")

		if n == 1 {
			_, _ = w.Write([]byte(`"first"`))
		} else {
			_, _ = w.Write([]byte(`"second"`))
		}
	}))
	defer srv.Close()

	transport := &Transport{Base: http.DefaultTransport}
	client := &http.Client{Transport: transport}

	resp := get(t, client, srv.URL+"/test")
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, `"first"`, string(body))

	resp = get(t, client, srv.URL+"/test")
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, `"second"`, string(body))
}

func TestTransport_NoCacheWithoutETag(t *testing.T) {
	var callCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		assert.Empty(t, r.Header.Get("If-None-Match"))

		_, _ = w.Write([]byte(`"ok"`))
	}))
	defer srv.Close()

	transport := &Transport{Base: http.DefaultTransport}
	client := &http.Client{Transport: transport}

	for range 3 {
		resp := get(t, client, srv.URL+"/nocache")
		_ = resp.Body.Close()
	}

	assert.Equal(t, int32(3), callCount.Load())
}

func TestTransport_DifferentURLsCachedSeparately(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"etag-`+r.URL.Path+`"`)
		_, _ = w.Write([]byte(r.URL.Path))
	}))
	defer srv.Close()

	transport := &Transport{Base: http.DefaultTransport}
	client := &http.Client{Transport: transport}

	resp1 := get(t, client, srv.URL+"/a")
	body1, _ := io.ReadAll(resp1.Body)
	_ = resp1.Body.Close()

	resp2 := get(t, client, srv.URL+"/b")
	body2, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()

	assert.Equal(t, "/a", string(body1))
	assert.Equal(t, "/b", string(body2))
}
