package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

type GeckoTradeReader struct {
	client  *http.Client
	baseURL string
	now     func() time.Time
	pacer   Pacer
}

func NewGeckoTradeReader(client *http.Client, baseURL string, now func() time.Time, pacers ...Pacer) *GeckoTradeReader {
	if baseURL == "" {
		baseURL = geckoTerminalBaseURL
	}
	var pacer Pacer
	if len(pacers) > 0 {
		pacer = pacers[0]
	}
	return &GeckoTradeReader{client: client, baseURL: strings.TrimRight(baseURL, "/"), now: now, pacer: pacer}
}

type geckoTradesResponse struct {
	Data []struct {
		Attributes struct {
			FromAddress    string `json:"tx_from_address"`
			Kind           string `json:"kind"`
			VolumeUSD      string `json:"volume_in_usd"`
			BlockTimestamp string `json:"block_timestamp"`
		} `json:"attributes"`
	} `json:"data"`
}

func (r *GeckoTradeReader) ReadRecentTrades(ctx context.Context, chain domain.Chain, poolAddress string) (domain.TradeWindow, error) {
	network, err := geckoNetwork(chain)
	if err != nil {
		return domain.TradeWindow{}, err
	}
	endpoint := fmt.Sprintf("%s/networks/%s/pools/%s/trades", r.baseURL, url.PathEscape(network), url.PathEscape(normalizeAddress(chain, poolAddress)))
	var response geckoTradesResponse
	if err := requestJSONPaced(ctx, r.client, "geckoterminal", endpoint, &response, r.pacer); err != nil {
		return domain.TradeWindow{}, err
	}
	returnedCount := len(response.Data)
	if len(response.Data) > 300 {
		response.Data = response.Data[:300]
	}
	collectedAt := providerNow(r.now)
	window := domain.TradeWindow{
		CollectedAt: collectedAt,
		Trades:      make([]domain.Trade, 0, len(response.Data)),
		Current:     domain.TradeStats{Start: collectedAt.Add(-5 * time.Minute), End: collectedAt},
		Previous:    domain.TradeStats{Start: collectedAt.Add(-10 * time.Minute), End: collectedAt.Add(-5 * time.Minute)},
		Truncated:   returnedCount >= 300,
	}
	for _, item := range response.Data {
		if strings.TrimSpace(item.Attributes.FromAddress) == "" {
			return domain.TradeWindow{}, badResponse("geckoterminal", http.StatusOK, fmt.Errorf("missing tx_from_address"))
		}
		kind := domain.TradeKind(item.Attributes.Kind)
		if kind != domain.TradeBuy && kind != domain.TradeSell {
			return domain.TradeWindow{}, badResponse("geckoterminal", http.StatusOK, fmt.Errorf("unknown trade kind %q", item.Attributes.Kind))
		}
		volume, err := strconv.ParseFloat(item.Attributes.VolumeUSD, 64)
		if err != nil {
			return domain.TradeWindow{}, badResponse("geckoterminal", http.StatusOK, fmt.Errorf("parse volume_in_usd: %w", err))
		}
		timestamp, err := time.Parse(time.RFC3339, item.Attributes.BlockTimestamp)
		if err != nil {
			return domain.TradeWindow{}, badResponse("geckoterminal", http.StatusOK, fmt.Errorf("parse block_timestamp: %w", err))
		}
		window.Trades = append(window.Trades, domain.Trade{
			FromAddress: normalizeAddress(chain, item.Attributes.FromAddress), Kind: kind,
			VolumeUSD: volume, BlockTimestamp: timestamp.UTC(),
		})
	}
	window.Current = aggregateTrades(window.Trades, window.Current.Start, window.Current.End, true)
	window.Previous = aggregateTrades(window.Trades, window.Previous.Start, window.Previous.End, false)
	return window, nil
}

func aggregateTrades(trades []domain.Trade, start, end time.Time, includeEnd bool) domain.TradeStats {
	stats := domain.TradeStats{Start: start, End: end}
	buyers := map[string]struct{}{}
	sellers := map[string]struct{}{}
	volumeByAddress := map[string]float64{}
	totalVolume := 0.0
	for _, trade := range trades {
		if trade.BlockTimestamp.Before(start) || trade.BlockTimestamp.After(end) || (!includeEnd && trade.BlockTimestamp.Equal(end)) {
			continue
		}
		switch trade.Kind {
		case domain.TradeBuy:
			stats.Buys++
			stats.BuyVolumeUSD += trade.VolumeUSD
			if trade.FromAddress != "" {
				buyers[trade.FromAddress] = struct{}{}
			}
		case domain.TradeSell:
			stats.Sells++
			stats.SellVolumeUSD += trade.VolumeUSD
			if trade.FromAddress != "" {
				sellers[trade.FromAddress] = struct{}{}
			}
		default:
			continue
		}
		if trade.FromAddress != "" {
			volumeByAddress[trade.FromAddress] += trade.VolumeUSD
		}
		totalVolume += trade.VolumeUSD
	}
	stats.UniqueBuyers = len(buyers)
	stats.UniqueSellers = len(sellers)
	if totalVolume > 0 {
		for _, volume := range volumeByAddress {
			stats.TopAddressVolumeShare = max(stats.TopAddressVolumeShare, volume/totalVolume)
		}
	}
	return stats
}
