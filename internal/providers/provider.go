package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

const (
	defaultHTTPTimeout = 8 * time.Second
	maxResponseBytes   = 2 * 1024 * 1024
	maxRetries         = 2
	maxRetryDelay      = 60 * time.Second
)

// Pacer spaces provider attempts without a resident goroutine.
type Pacer interface {
	Wait(context.Context) error
}

type RequestPriority int

const (
	RequestPriorityDeep RequestPriority = iota
	RequestPriorityDiscovery
)

type priorityPacer interface {
	WaitPriority(context.Context, RequestPriority) error
}

// RequestPacer reserves request slots at a fixed interval.
type RequestPacer struct {
	mu          sync.Mutex
	interval    time.Duration
	next        time.Time
	now         func() time.Time
	wait        func(context.Context, time.Duration) error
	wake        chan struct{}
	fastWaiters int
}

// NewRequestPacer returns a context-aware fixed-interval request pacer.
func NewRequestPacer(interval time.Duration) *RequestPacer {
	return &RequestPacer{interval: interval, now: time.Now, wait: waitContext, wake: make(chan struct{})}
}

// Wait blocks for a normal Deep request slot or context cancellation.
func (p *RequestPacer) Wait(ctx context.Context) error {
	return p.WaitPriority(ctx, RequestPriorityDeep)
}

// WaitPriority grants one shared slot at a time and lets waiting discovery proceed before Deep work.
func (p *RequestPacer) WaitPriority(ctx context.Context, priority RequestPriority) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p == nil || p.interval <= 0 {
		return nil
	}
	registeredFast := priority == RequestPriorityDiscovery
	if registeredFast {
		p.mu.Lock()
		p.fastWaiters++
		p.signalLocked()
		p.mu.Unlock()
	}
	defer func() {
		if registeredFast {
			p.mu.Lock()
			p.fastWaiters--
			p.signalLocked()
			p.mu.Unlock()
		}
	}()

	waited := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		p.mu.Lock()
		nowFn := p.now
		if nowFn == nil {
			nowFn = time.Now
		}
		now := nowFn()
		if priority != RequestPriorityDiscovery && p.fastWaiters > 0 {
			wake := p.wakeLocked()
			p.mu.Unlock()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-wake:
				continue
			}
		}
		delay := p.next.Sub(now)
		if delay <= 0 {
			p.next = now.Add(p.interval)
			if registeredFast {
				p.fastWaiters--
				registeredFast = false
			}
			p.signalLocked()
			wait := p.wait
			p.mu.Unlock()
			if waited {
				return nil
			}
			if wait == nil {
				wait = waitContext
			}
			return wait(ctx, 0)
		}
		wait := p.wait
		p.mu.Unlock()
		if wait == nil {
			wait = waitContext
		}
		if err := wait(ctx, delay); err != nil {
			return err
		}
		waited = true
	}
}

func (p *RequestPacer) wakeLocked() chan struct{} {
	if p.wake == nil {
		p.wake = make(chan struct{})
	}
	return p.wake
}

func (p *RequestPacer) signalLocked() {
	wake := p.wakeLocked()
	close(wake)
	p.wake = make(chan struct{})
}

type Discoverer interface {
	Discover(ctx context.Context, chain domain.Chain) ([]domain.Candidate, error)
}

// CoverageDiscoverer reports the actual provider window observed by discovery.
type CoverageDiscoverer interface {
	DiscoverWithCoverage(ctx context.Context, chain domain.Chain) (domain.DiscoveryBatch, error)
}

type Enricher interface {
	Enrich(ctx context.Context, c domain.Candidate) (domain.MarketSnapshot, error)
}

type TradeReader interface {
	ReadRecentTrades(ctx context.Context, chain domain.Chain, poolAddress string) (domain.TradeWindow, error)
}

type SocialProvider interface {
	Evidence(ctx context.Context, c domain.Candidate, m domain.MarketSnapshot) (domain.SocialEvidence, error)
}

type ErrorKind string

const (
	ErrorRateLimited ErrorKind = "rate_limited"
	ErrorTimeout     ErrorKind = "timeout"
	ErrorBadResponse ErrorKind = "bad_response"
)

// ProviderError lets the scanner degrade a source without terminating a scan.
type ProviderError struct {
	Kind       ErrorKind
	Provider   string
	StatusCode int
	Err        error
}

func (e *ProviderError) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("%s %s (status %d): %v", e.Provider, e.Kind, e.StatusCode, e.Err)
	}
	return fmt.Sprintf("%s %s: %v", e.Provider, e.Kind, e.Err)
}

func (e *ProviderError) Unwrap() error { return e.Err }

// NewHTTPClient returns the shared small-VPS transport profile.
func NewHTTPClient(timeout time.Duration) *http.Client {
	return newHTTPClient(timeout, http.ProxyFromEnvironment)
}

// NewHTTPClientWithProxy 创建显式代理客户端；空值仍遵循系统代理环境变量。
func NewHTTPClientWithProxy(timeout time.Duration, rawProxy string) (*http.Client, error) {
	if strings.TrimSpace(rawProxy) == "" {
		return NewHTTPClient(timeout), nil
	}
	proxyURL, err := url.Parse(strings.TrimSpace(rawProxy))
	if err != nil || proxyURL.Host == "" || (proxyURL.Scheme != "http" && proxyURL.Scheme != "https") {
		return nil, fmt.Errorf("invalid proxy URL %q (want http://host:port or https://host:port)", rawProxy)
	}
	return newHTTPClient(timeout, http.ProxyURL(proxyURL)), nil
}

func newHTTPClient(timeout time.Duration, proxy func(*http.Request) (*url.URL, error)) *http.Client {
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:               proxy,
			MaxIdleConns:        8,
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     90 * time.Second,
		},
	}
}

func requestJSON(ctx context.Context, client *http.Client, provider, endpoint string, dst any) error {
	return requestJSONPaced(ctx, client, provider, endpoint, dst, nil)
}

func requestJSONPaced(ctx context.Context, client *http.Client, provider, endpoint string, dst any, pacer Pacer) error {
	return requestJSONPacedPriority(ctx, client, provider, endpoint, dst, pacer, RequestPriorityDeep)
}

func requestJSONPacedPriority(ctx context.Context, client *http.Client, provider, endpoint string, dst any, pacer Pacer, priority RequestPriority) error {
	if client == nil {
		client = NewHTTPClient(0)
	}
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if pacer != nil {
			var err error
			if prioritized, ok := pacer.(priorityPacer); ok {
				err = prioritized.WaitPriority(ctx, priority)
			} else {
				err = pacer.Wait(ctx)
			}
			if err != nil {
				return err
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return badResponse(provider, 0, err)
		}
		req.Header.Set("User-Agent", "fomo-scanner/1.0")
		resp, err := client.Do(req)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return context.Canceled
			}
			kind := ErrorBadResponse
			var netErr net.Error
			if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
				kind = ErrorTimeout
			}
			return &ProviderError{Kind: kind, Provider: provider, Err: err}
		}

		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
		closeErr := resp.Body.Close()
		if readErr != nil {
			return badResponse(provider, resp.StatusCode, readErr)
		}
		if closeErr != nil {
			return badResponse(provider, resp.StatusCode, closeErr)
		}
		if len(body) > maxResponseBytes {
			return badResponse(provider, resp.StatusCode, fmt.Errorf("response exceeds %d bytes", maxResponseBytes))
		}

		if retryableStatus(resp.StatusCode) {
			if attempt < maxRetries {
				if err := waitContext(ctx, retryDelay(resp.Header.Get("Retry-After"), attempt)); err != nil {
					if errors.Is(err, context.Canceled) {
						return context.Canceled
					}
					return &ProviderError{Kind: ErrorTimeout, Provider: provider, StatusCode: resp.StatusCode, Err: err}
				}
				continue
			}
			kind := ErrorBadResponse
			if resp.StatusCode == http.StatusTooManyRequests {
				kind = ErrorRateLimited
			}
			return &ProviderError{Kind: kind, Provider: provider, StatusCode: resp.StatusCode, Err: fmt.Errorf("retryable response: %s", strings.TrimSpace(string(body)))}
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return badResponse(provider, resp.StatusCode, fmt.Errorf("unexpected response: %s", strings.TrimSpace(string(body))))
		}
		if err := json.Unmarshal(body, dst); err != nil {
			return badResponse(provider, resp.StatusCode, fmt.Errorf("decode JSON: %w", err))
		}
		return nil
	}
	panic("unreachable")
}

func badResponse(provider string, status int, err error) error {
	return &ProviderError{Kind: ErrorBadResponse, Provider: provider, StatusCode: status, Err: err}
}

func retryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}

func retryDelay(retryAfter string, attempt int) time.Duration {
	return retryDelayAt(retryAfter, attempt, time.Now())
}

func retryDelayAt(retryAfter string, attempt int, now time.Time) time.Duration {
	if retryAfter != "" {
		if seconds, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && seconds >= 0 {
			return min(time.Duration(seconds)*time.Second, maxRetryDelay)
		}
		if when, err := http.ParseTime(retryAfter); err == nil {
			return min(max(when.Sub(now), 0), maxRetryDelay)
		}
	}
	return min(time.Duration(100*(1<<attempt))*time.Millisecond, maxRetryDelay)
}

func waitContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func providerNow(now func() time.Time) time.Time {
	if now == nil {
		return time.Now().UTC()
	}
	return now().UTC()
}

func normalizeAddress(chain domain.Chain, address string) string {
	if chain == domain.ChainBSC {
		return strings.ToLower(address)
	}
	return address
}
