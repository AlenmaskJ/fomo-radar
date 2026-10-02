package providers

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

const defaultOKXBaseURL = "https://www.okx.com"

type OKXMarketResult struct {
	Snapshots   []domain.DerivativesSnapshot
	Instruments map[string]domain.MarketInstrument
	Warnings    []string
}

// OKXMarketReader 通过四个批量公共接口读取行情，不需要 API Key。
type OKXMarketReader struct {
	client  *http.Client
	baseURL string
	now     func() time.Time
}

func NewOKXMarketReader(client *http.Client, baseURL string, now func() time.Time) *OKXMarketReader {
	if client == nil {
		client = NewHTTPClient(0)
	}
	if baseURL == "" {
		baseURL = defaultOKXBaseURL
	}
	if now == nil {
		now = time.Now
	}
	return &OKXMarketReader{client: client, baseURL: strings.TrimRight(baseURL, "/"), now: now}
}

func (r *OKXMarketReader) Read(ctx context.Context) (OKXMarketResult, error) {
	var instruments okxInstrumentsResponse
	if err := requestJSON(ctx, r.client, "okx", r.baseURL+"/api/v5/public/instruments?instType=SWAP", &instruments); err != nil {
		return OKXMarketResult{}, fmt.Errorf("read instruments: %w", err)
	}
	if err := instruments.validate(); err != nil {
		return OKXMarketResult{}, err
	}

	var tickers okxTickersResponse
	if err := requestJSON(ctx, r.client, "okx", r.baseURL+"/api/v5/market/tickers?instType=SWAP", &tickers); err != nil {
		return OKXMarketResult{}, fmt.Errorf("read tickers: %w", err)
	}
	if err := tickers.validate(); err != nil {
		return OKXMarketResult{}, err
	}

	warnings := make([]string, 0, 2)
	openInterest := map[string]float64{}
	var oiResponse okxOpenInterestResponse
	if err := requestJSON(ctx, r.client, "okx", r.baseURL+"/api/v5/public/open-interest?instType=SWAP", &oiResponse); err != nil {
		warnings = append(warnings, "open interest: "+err.Error())
	} else if err := oiResponse.validate(); err != nil {
		warnings = append(warnings, "open interest: "+err.Error())
	} else {
		for _, item := range oiResponse.Data {
			if value, err := strconv.ParseFloat(item.OpenInterestUSD, 64); err == nil {
				openInterest[item.InstrumentID] = value
			}
		}
	}

	funding := map[string]float64{}
	var fundingResponse okxFundingResponse
	if err := requestJSON(ctx, r.client, "okx", r.baseURL+"/api/v5/public/funding-rate?instId=ANY", &fundingResponse); err != nil {
		warnings = append(warnings, "funding rate: "+err.Error())
	} else if err := fundingResponse.validate(); err != nil {
		warnings = append(warnings, "funding rate: "+err.Error())
	} else {
		for _, item := range fundingResponse.Data {
			if value, err := strconv.ParseFloat(item.FundingRate, 64); err == nil {
				funding[item.InstrumentID] = value
			}
		}
	}

	tickerByID := make(map[string]okxTicker, len(tickers.Data))
	for _, ticker := range tickers.Data {
		tickerByID[ticker.InstrumentID] = ticker
	}
	collectedAt := r.now().UTC()
	snapshots := make([]domain.DerivativesSnapshot, 0, len(instruments.Data))
	metadata := make(map[string]domain.MarketInstrument, len(instruments.Data))
	for _, instrument := range instruments.Data {
		assetClass, ok := okxAssetClass(instrument)
		if !ok {
			continue
		}
		ticker, ok := tickerByID[instrument.InstrumentID]
		if !ok {
			continue
		}
		snapshot, err := okxSnapshot(instrument, ticker, assetClass, collectedAt)
		if err != nil {
			warnings = append(warnings, err.Error())
			continue
		}
		if value, ok := openInterest[instrument.InstrumentID]; ok {
			snapshot.OpenInterestUSD = value
			snapshot.OpenInterestAvailable = true
		}
		if value, ok := funding[instrument.InstrumentID]; ok {
			snapshot.FundingRate = value
			snapshot.FundingRateAvailable = true
		}
		snapshots = append(snapshots, snapshot)
		listedMillis, listErr := strconv.ParseInt(instrument.ListTime, 10, 64)
		tickSize, tickErr := strconv.ParseFloat(instrument.TickSize, 64)
		if listErr != nil || listedMillis <= 0 || tickErr != nil || tickSize <= 0 || math.IsNaN(tickSize) || math.IsInf(tickSize, 0) {
			warnings = append(warnings, fmt.Sprintf("instrument %s has invalid listTime/tickSz", instrument.InstrumentID))
			continue
		}
		metadata[instrument.InstrumentID] = domain.MarketInstrument{
			InstrumentID: instrument.InstrumentID,
			Symbol:       snapshot.Symbol,
			ListedAt:     time.UnixMilli(listedMillis).UTC(),
			TickSize:     tickSize,
		}
	}
	return OKXMarketResult{Snapshots: snapshots, Instruments: metadata, Warnings: warnings}, nil
}

func okxAssetClass(instrument okxInstrument) (domain.AssetClass, bool) {
	if instrument.State != "live" || instrument.RuleType == "pre_market" {
		return "", false
	}
	switch instrument.Category {
	case "1":
		return domain.AssetClassCrypto, true
	case "3":
		return domain.AssetClassStock, true
	default:
		return "", false
	}
}

func okxSnapshot(instrument okxInstrument, ticker okxTicker, assetClass domain.AssetClass, collectedAt time.Time) (domain.DerivativesSnapshot, error) {
	last, err := strconv.ParseFloat(ticker.Last, 64)
	if err != nil || last <= 0 {
		return domain.DerivativesSnapshot{}, fmt.Errorf("ticker %s has invalid last price %q", instrument.InstrumentID, ticker.Last)
	}
	open, err := strconv.ParseFloat(ticker.Open24h, 64)
	if err != nil || open <= 0 {
		return domain.DerivativesSnapshot{}, fmt.Errorf("ticker %s has invalid 24h open %q", instrument.InstrumentID, ticker.Open24h)
	}
	volume, err := strconv.ParseFloat(ticker.VolumeCurrency24h, 64)
	if err != nil || volume < 0 {
		return domain.DerivativesSnapshot{}, fmt.Errorf("ticker %s has invalid 24h volume %q", instrument.InstrumentID, ticker.VolumeCurrency24h)
	}
	sourceMillis, err := strconv.ParseInt(ticker.Timestamp, 10, 64)
	if err != nil {
		return domain.DerivativesSnapshot{}, fmt.Errorf("ticker %s has invalid timestamp %q", instrument.InstrumentID, ticker.Timestamp)
	}
	symbol := instrument.BaseCurrency
	if symbol == "" {
		symbol, _, _ = strings.Cut(instrument.InstrumentID, "-")
	}
	return domain.DerivativesSnapshot{
		AssetClass:     assetClass,
		InstrumentID:   instrument.InstrumentID,
		Symbol:         symbol,
		CollectedAt:    collectedAt,
		SourceTime:     time.UnixMilli(sourceMillis).UTC(),
		PriceUSD:       last,
		Change24hPct:   (last/open - 1) * 100,
		Turnover24hUSD: volume * last,
	}, nil
}

type okxEnvelope struct {
	Code    string `json:"code"`
	Message string `json:"msg"`
}

func (e okxEnvelope) validate() error {
	if e.Code != "0" {
		return fmt.Errorf("okx response code %s: %s", e.Code, e.Message)
	}
	return nil
}

type okxInstrumentsResponse struct {
	okxEnvelope
	Data []okxInstrument `json:"data"`
}

type okxInstrument struct {
	InstrumentID string `json:"instId"`
	BaseCurrency string `json:"baseCcy"`
	Category     string `json:"instCategory"`
	RuleType     string `json:"ruleType"`
	State        string `json:"state"`
	ListTime     string `json:"listTime"`
	TickSize     string `json:"tickSz"`
}

type okxTickersResponse struct {
	okxEnvelope
	Data []okxTicker `json:"data"`
}

type okxTicker struct {
	InstrumentID      string `json:"instId"`
	Last              string `json:"last"`
	Open24h           string `json:"open24h"`
	VolumeCurrency24h string `json:"volCcy24h"`
	Timestamp         string `json:"ts"`
}

type okxOpenInterestResponse struct {
	okxEnvelope
	Data []struct {
		InstrumentID    string `json:"instId"`
		OpenInterestUSD string `json:"oiUsd"`
	} `json:"data"`
}

type okxFundingResponse struct {
	okxEnvelope
	Data []struct {
		InstrumentID string `json:"instId"`
		FundingRate  string `json:"fundingRate"`
	} `json:"data"`
}
