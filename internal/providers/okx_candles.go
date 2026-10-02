package providers

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
)

// OKXCandleReader 读取无需鉴权的 OKX K 线，并共享请求节流器。
type OKXCandleReader struct {
	client  *http.Client
	baseURL string
	pacer   Pacer
}

func NewOKXCandleReader(client *http.Client, baseURL string, pacer Pacer) *OKXCandleReader {
	if client == nil {
		client = NewHTTPClient(0)
	}
	if baseURL == "" {
		baseURL = defaultOKXBaseURL
	}
	return &OKXCandleReader{client: client, baseURL: strings.TrimRight(baseURL, "/"), pacer: pacer}
}

func (r *OKXCandleReader) Read(ctx context.Context, instrumentID string, timeframe domain.Timeframe, limit int) ([]domain.Candle, error) {
	duration, ok := timeframeDuration(timeframe)
	if !ok {
		return nil, badResponse("okx", 0, fmt.Errorf("unsupported timeframe %q", timeframe))
	}
	if strings.TrimSpace(instrumentID) == "" {
		return nil, badResponse("okx", 0, fmt.Errorf("empty instrument ID"))
	}
	if limit <= 0 {
		limit = 300
	}
	limit = min(limit, 300)
	query := url.Values{
		"instId": []string{instrumentID},
		"bar":    []string{string(timeframe)},
		"limit":  []string{strconv.Itoa(limit)},
	}
	var response okxCandlesResponse
	endpoint := r.baseURL + "/api/v5/market/candles?" + query.Encode()
	if err := requestJSONPaced(ctx, r.client, "okx", endpoint, &response, r.pacer); err != nil {
		return nil, err
	}
	if err := response.validate(); err != nil {
		return nil, badResponse("okx", 0, err)
	}
	candles := make([]domain.Candle, 0, len(response.Data))
	for _, row := range response.Data {
		candle, err := parseOKXCandle(row, duration)
		if err != nil {
			return nil, badResponse("okx", 0, err)
		}
		candles = append(candles, candle)
	}
	sort.Slice(candles, func(i, j int) bool { return candles[i].OpenTime.Before(candles[j].OpenTime) })
	for index := 1; index < len(candles); index++ {
		if !candles[index-1].OpenTime.Before(candles[index].OpenTime) {
			return nil, badResponse("okx", 0, fmt.Errorf("duplicate or unsorted candle timestamp"))
		}
	}
	return candles, nil
}

func timeframeDuration(timeframe domain.Timeframe) (time.Duration, bool) {
	switch timeframe {
	case domain.Timeframe15m:
		return 15 * time.Minute, true
	case domain.Timeframe1H:
		return time.Hour, true
	case domain.Timeframe4H:
		return 4 * time.Hour, true
	case domain.Timeframe1D:
		return 24 * time.Hour, true
	default:
		return 0, false
	}
}

func parseOKXCandle(row []string, duration time.Duration) (domain.Candle, error) {
	if len(row) != 9 {
		return domain.Candle{}, fmt.Errorf("candle has %d fields, want 9", len(row))
	}
	millis, err := strconv.ParseInt(row[0], 10, 64)
	if err != nil || millis <= 0 {
		return domain.Candle{}, fmt.Errorf("invalid candle timestamp %q", row[0])
	}
	values := make([]float64, 7)
	for index := range values {
		values[index], err = strconv.ParseFloat(row[index+1], 64)
		if err != nil || math.IsNaN(values[index]) || math.IsInf(values[index], 0) {
			return domain.Candle{}, fmt.Errorf("invalid candle field %d %q", index+1, row[index+1])
		}
	}
	if values[0] <= 0 || values[1] <= 0 || values[2] <= 0 || values[3] <= 0 || values[4] < 0 || values[5] < 0 || values[6] < 0 || values[1] < values[2] {
		return domain.Candle{}, fmt.Errorf("invalid candle values")
	}
	if row[8] != "0" && row[8] != "1" {
		return domain.Candle{}, fmt.Errorf("invalid candle confirmation %q", row[8])
	}
	openedAt := time.UnixMilli(millis).UTC()
	return domain.Candle{
		OpenTime: openedAt, CloseTime: openedAt.Add(duration),
		Open: values[0], High: values[1], Low: values[2], Close: values[3],
		VolumeBase: values[4], VolumeQuote: values[6], Confirmed: row[8] == "1",
	}, nil
}

type okxCandlesResponse struct {
	okxEnvelope
	Data [][]string `json:"data"`
}
