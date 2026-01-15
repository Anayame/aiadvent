package retry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type recordSleeper struct {
	delays []time.Duration
}

func (s *recordSleeper) Sleep(ctx context.Context, d time.Duration) error {
	s.delays = append(s.delays, d)
	return nil
}

func doRequest(t *testing.T, client *http.Client, url string) func(ctx context.Context) (*http.Response, []byte, error) {
	t.Helper()
	return func(ctx context.Context) (*http.Response, []byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
		if err != nil {
			return nil, nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, nil, err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return resp, nil, err
		}
		return resp, body, nil
	}
}

func TestRetry429WithJitterRange(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("rate limit"))
	}))
	t.Cleanup(server.Close)

	sleep := &recordSleeper{}
	policy := Policy{
		BaseDelay:      500 * time.Millisecond,
		MaxDelay:       8 * time.Second,
		Multiplier:     2.0,
		MaxAttempts:    2,
		JitterFraction: 0.30,
		SnippetLimit:   200,
		Sleep:          sleep.Sleep,
		Now:            time.Now,
		Rand:           func() float64 { return 0.0 },
	}

	_, _, err := DoHTTP(context.Background(), policy, nil, doRequest(t, server.Client(), server.URL))
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if calls != 2 {
		t.Fatalf("expected 2 calls, got %d", calls)
	}
	if len(sleep.delays) != 1 {
		t.Fatalf("expected 1 sleep, got %d", len(sleep.delays))
	}
	delay := sleep.delays[0]
	if delay < 350*time.Millisecond || delay > 650*time.Millisecond {
		t.Fatalf("delay out of jitter range: %s", delay)
	}
}

func TestRetry429WithRetryAfterSeconds(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("rate limit"))
	}))
	t.Cleanup(server.Close)

	sleep := &recordSleeper{}
	policy := Policy{
		BaseDelay:      500 * time.Millisecond,
		MaxDelay:       8 * time.Second,
		Multiplier:     2.0,
		MaxAttempts:    2,
		JitterFraction: 0.30,
		SnippetLimit:   200,
		Sleep:          sleep.Sleep,
		Now:            time.Now,
		Rand:           func() float64 { return 0.5 },
	}

	_, _, err := DoHTTP(context.Background(), policy, nil, doRequest(t, server.Client(), server.URL))
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if calls != 2 {
		t.Fatalf("expected 2 calls, got %d", calls)
	}
	if len(sleep.delays) != 1 {
		t.Fatalf("expected 1 sleep, got %d", len(sleep.delays))
	}
	if sleep.delays[0] != 2*time.Second {
		t.Fatalf("expected retry-after 2s, got %s", sleep.delays[0])
	}
}

func TestRetry503ThenSuccess(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("overloaded"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(server.Close)

	sleep := &recordSleeper{}
	policy := Policy{
		BaseDelay:      500 * time.Millisecond,
		MaxDelay:       8 * time.Second,
		Multiplier:     2.0,
		MaxAttempts:    3,
		JitterFraction: 0.30,
		SnippetLimit:   200,
		Sleep:          sleep.Sleep,
		Now:            time.Now,
		Rand:           func() float64 { return 0.5 },
	}

	resp, body, err := DoHTTP(context.Background(), policy, nil, doRequest(t, server.Client(), server.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
	if len(sleep.delays) != 2 {
		t.Fatalf("expected 2 sleeps, got %d", len(sleep.delays))
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if string(body) != "ok" {
		t.Fatalf("unexpected body: %s", string(body))
	}
}

func TestNoRetryOn400(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("bad request"))
	}))
	t.Cleanup(server.Close)

	sleep := &recordSleeper{}
	policy := Policy{
		BaseDelay:      500 * time.Millisecond,
		MaxDelay:       8 * time.Second,
		Multiplier:     2.0,
		MaxAttempts:    3,
		JitterFraction: 0.30,
		SnippetLimit:   200,
		Sleep:          sleep.Sleep,
		Now:            time.Now,
		Rand:           func() float64 { return 0.5 },
	}

	resp, _, err := DoHTTP(context.Background(), policy, nil, doRequest(t, server.Client(), server.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
	if len(sleep.delays) != 0 {
		t.Fatalf("expected 0 sleeps, got %d", len(sleep.delays))
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestContextCancelStopsRetry(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("overloaded"))
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	sleepFunc := func(ctx context.Context, d time.Duration) error {
		cancel()
		return ctx.Err()
	}

	policy := Policy{
		BaseDelay:      500 * time.Millisecond,
		MaxDelay:       8 * time.Second,
		Multiplier:     2.0,
		MaxAttempts:    3,
		JitterFraction: 0.30,
		SnippetLimit:   200,
		Sleep:          sleepFunc,
		Now:            time.Now,
		Rand:           func() float64 { return 0.5 },
	}

	_, _, err := DoHTTP(ctx, policy, nil, doRequest(t, server.Client(), server.URL))
	if err == nil {
		t.Fatalf("expected context error, got nil")
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
}
