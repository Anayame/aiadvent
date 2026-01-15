package retry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultBaseDelay      = 500 * time.Millisecond
	defaultMaxDelay       = 8 * time.Second
	defaultMultiplier     = 2.0
	defaultMaxAttempts    = 6
	defaultJitterFraction = 0.30
	defaultSnippetLimit   = 200
)

type Sleeper func(ctx context.Context, d time.Duration) error
type NowFunc func() time.Time
type RandFunc func() float64

type Policy struct {
	BaseDelay      time.Duration
	MaxDelay       time.Duration
	Multiplier     float64
	MaxAttempts    int
	JitterFraction float64
	SnippetLimit   int
	Sleep          Sleeper
	Now            NowFunc
	Rand           RandFunc
}

func DefaultPolicy() Policy {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	return Policy{
		BaseDelay:      defaultBaseDelay,
		MaxDelay:       defaultMaxDelay,
		Multiplier:     defaultMultiplier,
		MaxAttempts:    defaultMaxAttempts,
		JitterFraction: defaultJitterFraction,
		SnippetLimit:   defaultSnippetLimit,
		Sleep:          defaultSleep,
		Now:            time.Now,
		Rand:           rng.Float64,
	}
}

type HTTPStatusError struct {
	StatusCode  int
	BodySnippet string
}

func (e *HTTPStatusError) Error() string {
	if e.BodySnippet == "" {
		return fmt.Sprintf("transient status %d", e.StatusCode)
	}
	return fmt.Sprintf("transient status %d: %s", e.StatusCode, e.BodySnippet)
}

type ExhaustedError struct {
	Cause    error
	Attempts int
}

func (e *ExhaustedError) Error() string {
	return fmt.Sprintf("retry attempts exhausted after %d: %v", e.Attempts, e.Cause)
}

func (e *ExhaustedError) Unwrap() error {
	return e.Cause
}

func DoHTTP(ctx context.Context, policy Policy, logger *slog.Logger, do func(ctx context.Context) (*http.Response, []byte, error)) (*http.Response, []byte, error) {
	policy = withDefaults(policy)

	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}

		resp, body, err := do(ctx)
		if err != nil {
			if !isRetryableNetErr(ctx, err) || attempt == policy.MaxAttempts {
				if attempt == policy.MaxAttempts && isRetryableNetErr(ctx, err) {
					return resp, body, &ExhaustedError{Cause: err, Attempts: attempt}
				}
				return resp, body, err
			}
			delay := policy.jitterDelay(policy.backoffDelay(attempt))
			reason := reasonForNetErr(err)
			logRetry(logger, attempt+1, policy.MaxAttempts, 0, reason, delay, false, "")
			if err := policy.Sleep(ctx, delay); err != nil {
				return nil, nil, err
			}
			continue
		}

		if resp == nil {
			return nil, nil, errors.New("nil response from http client")
		}

		status := resp.StatusCode
		if isRetryableStatus(status) {
			snippet := bodySnippet(body, policy.SnippetLimit)
			if attempt == policy.MaxAttempts {
				return resp, body, &ExhaustedError{
					Cause:    &HTTPStatusError{StatusCode: status, BodySnippet: snippet},
					Attempts: attempt,
				}
			}

			retryAfter, usedRetryAfter := parseRetryAfter(resp.Header, policy.Now())
			delay := policy.nextDelay(attempt, retryAfter, usedRetryAfter)
			reason := reasonForStatus(status)
			logRetry(logger, attempt+1, policy.MaxAttempts, status, reason, delay, usedRetryAfter, snippet)
			if err := policy.Sleep(ctx, delay); err != nil {
				return nil, nil, err
			}
			continue
		}

		return resp, body, nil
	}

	return nil, nil, errors.New("retry attempts exhausted")
}

func withDefaults(p Policy) Policy {
	if p.BaseDelay == 0 {
		p.BaseDelay = defaultBaseDelay
	}
	if p.MaxDelay == 0 {
		p.MaxDelay = defaultMaxDelay
	}
	if p.Multiplier == 0 {
		p.Multiplier = defaultMultiplier
	}
	if p.MaxAttempts == 0 {
		p.MaxAttempts = defaultMaxAttempts
	}
	if p.JitterFraction == 0 {
		p.JitterFraction = defaultJitterFraction
	}
	if p.SnippetLimit == 0 {
		p.SnippetLimit = defaultSnippetLimit
	}
	if p.Sleep == nil {
		p.Sleep = defaultSleep
	}
	if p.Now == nil {
		p.Now = time.Now
	}
	if p.Rand == nil {
		rng := rand.New(rand.NewSource(time.Now().UnixNano()))
		p.Rand = rng.Float64
	}
	return p
}

func (p Policy) backoffDelay(retryIndex int) time.Duration {
	if retryIndex < 1 {
		retryIndex = 1
	}
	delay := float64(p.BaseDelay) * math.Pow(p.Multiplier, float64(retryIndex-1))
	if delay > float64(p.MaxDelay) {
		delay = float64(p.MaxDelay)
	}
	return time.Duration(delay)
}

func (p Policy) jitterDelay(delay time.Duration) time.Duration {
	if delay <= 0 || p.JitterFraction <= 0 {
		return delay
	}
	// Percentage jitter: +/- JitterFraction to reduce thundering herd.
	factor := 1 + (p.Rand()*2-1)*p.JitterFraction
	adjusted := float64(delay) * factor
	if adjusted < 0 {
		adjusted = 0
	}
	return time.Duration(adjusted)
}

func (p Policy) nextDelay(retryIndex int, retryAfter time.Duration, usedRetryAfter bool) time.Duration {
	if usedRetryAfter {
		return minDuration(retryAfter, p.MaxDelay)
	}
	return p.jitterDelay(p.backoffDelay(retryIndex))
}

func defaultSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func parseRetryAfter(header http.Header, now time.Time) (time.Duration, bool) {
	value := strings.TrimSpace(header.Get("Retry-After"))
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 {
			return 0, true
		}
		return time.Duration(seconds) * time.Second, true
	}
	if parsed, err := http.ParseTime(value); err == nil {
		delay := parsed.Sub(now)
		if delay < 0 {
			delay = 0
		}
		return delay, true
	}
	return 0, false
}

func isRetryableStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func reasonForStatus(status int) string {
	switch status {
	case http.StatusTooManyRequests:
		return "rate limit"
	case http.StatusRequestTimeout:
		return "timeout"
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return "upstream 5xx"
	default:
		return "http error"
	}
}

func isRetryableNetErr(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() || netErr.Temporary() {
			return true
		}
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	errMsg := strings.ToLower(err.Error())
	return strings.Contains(errMsg, "connection reset")
}

func reasonForNetErr(err error) string {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "eof"
	}
	if errors.Is(err, syscall.ECONNRESET) || strings.Contains(strings.ToLower(err.Error()), "connection reset") {
		return "connection reset"
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return "timeout"
		}
		if netErr.Temporary() {
			return "temporary network error"
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "network error"
}

func logRetry(logger *slog.Logger, attempt int, maxAttempts int, status int, reason string, delay time.Duration, usedRetryAfter bool, snippet string) {
	if logger == nil {
		return
	}
	attrs := []slog.Attr{
		slog.Int("attempt", attempt),
		slog.Int("max_attempts", maxAttempts),
		slog.String("reason", reason),
		slog.Duration("retry_in", delay),
		slog.Bool("retry_after_used", usedRetryAfter),
	}
	if status > 0 {
		attrs = append(attrs, slog.Int("status", status))
	}
	if snippet != "" {
		attrs = append(attrs, slog.String("snippet", snippet))
	}
	args := make([]any, 0, len(attrs))
	for _, attr := range attrs {
		args = append(args, attr)
	}
	logger.Warn("retrying request", args...)
}

func bodySnippet(body []byte, limit int) string {
	if len(body) == 0 || limit <= 0 {
		return ""
	}
	if len(body) <= limit {
		return string(body)
	}
	return string(body[:limit])
}

func minDuration(a, b time.Duration) time.Duration {
	if a <= b {
		return a
	}
	return b
}
