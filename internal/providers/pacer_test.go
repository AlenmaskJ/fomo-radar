package providers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

func TestGeckoDiscovererAndTradesShareRequestPacer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[],"included":[]}`))
	}))
	defer server.Close()

	clock := time.Date(2026, time.August, 16, 9, 0, 0, 0, time.UTC)
	waits := make([]time.Duration, 0, 2)
	pacer := &RequestPacer{
		interval: 6 * time.Second,
		now:      func() time.Time { return clock },
		wait: func(_ context.Context, delay time.Duration) error {
			waits = append(waits, delay)
			clock = clock.Add(delay)
			return nil
		},
	}
	discoverer := NewGeckoTerminalDiscoverer(NewHTTPClient(time.Second), server.URL, time.Now, pacer)
	reader := NewGeckoTradeReader(NewHTTPClient(time.Second), server.URL, time.Now, pacer)
	if _, err := discoverer.Discover(context.Background(), domain.ChainBSC); err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if _, err := reader.ReadRecentTrades(context.Background(), domain.ChainSolana, "pool"); err != nil {
		t.Fatalf("ReadRecentTrades() error = %v", err)
	}
	if len(waits) != 2 || waits[0] != 0 || waits[1] != 6*time.Second {
		t.Fatalf("shared pacer waits = %v, want [0s 6s]", waits)
	}
}

func TestRequestPacerCoversEveryRetryAttempt(t *testing.T) {
	serverAttempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverAttempts++
		if serverAttempts < 3 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "retry", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	pacer := &countingPacer{}
	var decoded []any
	if err := requestJSONPaced(context.Background(), NewHTTPClient(time.Second), "geckoterminal", server.URL, &decoded, pacer); err != nil {
		t.Fatalf("requestJSONPaced() error = %v", err)
	}
	if pacer.calls != 3 || serverAttempts != 3 {
		t.Fatalf("pacer/server attempts = %d/%d, want 3/3", pacer.calls, serverAttempts)
	}
}

func TestRequestPacerCancellationExitsPromptly(t *testing.T) {
	pacer := NewRequestPacer(time.Hour)
	if err := pacer.Wait(context.Background()); err != nil {
		t.Fatalf("first Wait() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	err := pacer.Wait(ctx)
	if !errors.Is(err, context.Canceled) || time.Since(started) > 100*time.Millisecond {
		t.Fatalf("Wait() error/elapsed = %v/%s, want prompt context cancellation", err, time.Since(started))
	}
}

func TestRequestPacerGivesWaitingDiscoveryTheNextSharedSlot(t *testing.T) {
	pacer := NewRequestPacer(20 * time.Millisecond)
	if err := pacer.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	completed := make(chan string, 5)
	var workers sync.WaitGroup
	for index := 0; index < 4; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if err := pacer.Wait(context.Background()); err != nil {
				completed <- "deep-error"
				return
			}
			completed <- "deep"
		}()
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		if err := pacer.WaitPriority(context.Background(), RequestPriorityDiscovery); err != nil {
			completed <- "discovery-error"
			return
		}
		completed <- "discovery"
	}()

	select {
	case first := <-completed:
		if first != "discovery" {
			t.Fatalf("next shared slot completed %q, want discovery before queued Deep", first)
		}
	case <-time.After(time.Second):
		t.Fatal("shared pacer did not grant next slot")
	}
	workers.Wait()
}

func TestRetryAfterHonorsNonzeroSecondsAndHTTPDate(t *testing.T) {
	now := time.Date(2026, time.August, 16, 8, 0, 0, 0, time.UTC)
	if got := retryDelayAt("30", 0, now); got != 30*time.Second {
		t.Fatalf("seconds Retry-After = %s, want 30s", got)
	}
	when := now.Add(45 * time.Second).Format(http.TimeFormat)
	if got := retryDelayAt(when, 0, now); got != 45*time.Second {
		t.Fatalf("date Retry-After = %s, want 45s", got)
	}
}

type countingPacer struct{ calls int }

func (p *countingPacer) Wait(context.Context) error {
	p.calls++
	return nil
}
