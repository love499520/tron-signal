package source

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"tron-signal/internal/block"
)

// TronGrid
// TronGrid HTTP 数据源（getnowblock 风格）
type TronGrid struct {
	id        string
	name      string
	endpoint  string
	headers   map[string]string
	enabled   bool
	baseRate  int
	maxRate   int
	lastCost  time.Duration
	client    *http.Client
}

func NewTronGrid(id, name, endpoint string, headers map[string]string, enabled bool, baseRate, maxRate int) *TronGrid {
	return &TronGrid{
		id:       id,
		name:     name,
		endpoint: endpoint,
		headers:  headers,
		enabled:  enabled,
		baseRate: baseRate,
		maxRate:  maxRate,
		client: &http.Client{
			Timeout: 8 * time.Second,
		},
	}
}

func (t *TronGrid) ID() string            { return t.id }
func (t *TronGrid) Name() string          { return t.name }
func (t *TronGrid) Enabled() bool         { return t.enabled }
func (t *TronGrid) BaseRate() int         { return t.baseRate }
func (t *TronGrid) MaxRate() int          { return t.maxRate }
func (t *TronGrid) LastLatency() time.Duration { return t.lastCost }

func (t *TronGrid) MarkError(err error) {
	// 降频/冷却逻辑由 scheduler 统一处理
}

func (t *TronGrid) FetchLatest(ctx context.Context) (*block.Meta, error) {
	start := time.Now()

	req, err := http.NewRequestWithContext(ctx, "GET", t.endpoint, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var raw struct {
		BlockID     string `json:"blockID"`
		BlockHeader struct {
			RawData struct {
				Number    int64 `json:"number"`
				Timestamp int64 `json:"timestamp"`
			} `json:"raw_data"`
		} `json:"block_header"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}

	if raw.BlockHeader.RawData.Number == 0 || raw.BlockID == "" {
		return nil, errors.New("invalid block data")
	}

	t.lastCost = time.Since(start)

	return &block.Meta{
		Height: raw.BlockHeader.RawData.Number,
		Hash:   raw.BlockID,
		Time:   time.Unix(raw.BlockHeader.RawData.Timestamp/1000, 0),
		Source: t.name,
	}, nil
}
