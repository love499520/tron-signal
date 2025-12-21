package source

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// AnkrRestFetcher
// 用于 TRON 原生 REST 风格接口（例如 getnowblock / latest block）
// endpoint 由用户配置；headers 由用户配置（可用于 API Key / Bearer / 自定义 Header）
type AnkrRestFetcher struct {
	cfg     Config
	limiter *Limiter
	client  *http.Client
}

func NewAnkrRestFetcher(cfg Config) *AnkrRestFetcher {
	return &AnkrRestFetcher{
		cfg:     cfg,
		limiter: NewLimiter(cfg.BaseRate, cfg.MaxRate),
		client: &http.Client{
			Timeout: 6 * time.Second,
		},
	}
}

func (a *AnkrRestFetcher) ID() string      { return a.cfg.ID }
func (a *AnkrRestFetcher) Config() *Config { return &a.cfg }

func (a *AnkrRestFetcher) UpdateConfig(cfg Config) {
	a.cfg = cfg
	a.limiter.Update(cfg.BaseRate, cfg.MaxRate)
}

func (a *AnkrRestFetcher) FetchLatest(ctx context.Context) (*Block, error) {
	if !a.cfg.Enabled {
		return nil, errors.New("disabled")
	}
	if !a.limiter.Allow() {
		// 达到上限：上层需要记录日志并跳过该源
		return nil, errors.New("rate_limited")
	}

	req, err := http.NewRequestWithContext(ctx, "GET", a.cfg.Endpoint, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range a.cfg.Headers {
		req.Header.Set(k, v)
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// TRON 标准字段（兼容 TronGrid/getnowblock 一类返回）
	var raw struct {
		BlockID     string `json:"blockID"`
		BlockHeader struct {
			RawData struct {
				Number    int64 `json:"number"`
				Timestamp int64 `json:"timestamp"` // ms
			} `json:"raw_data"`
		} `json:"block_header"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	if raw.BlockID == "" || raw.BlockHeader.RawData.Number <= 0 {
		return nil, errors.New("invalid_block")
	}

	return &Block{
		Height:  itoa64(raw.BlockHeader.RawData.Number),
		Hash:    raw.BlockID,
		Time:    time.Unix(raw.BlockHeader.RawData.Timestamp/1000, 0),
		Source:  "ankr-rest",
	}, nil
}

// 避免引入 strconv 在多个文件重复写
func itoa64(v int64) string {
	// 简单实现，减少依赖
	// 负数不考虑（区块高度不会负）
	if v == 0 {
		return "0"
	}
	var b [32]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + (v % 10))
		v /= 10
	}
	return string(b[i:])
}
