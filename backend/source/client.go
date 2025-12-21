package source

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"tron-signal/backend/block"
)

type Client struct {
	http *http.Client
}

func NewClient() *Client {
	return &Client{
		http: &http.Client{},
	}
}

func (c *Client) FetchLatest(ctx context.Context, cfg Config) (*block.Block, error) {
	method := cfg.Method
	if method == "" {
		method = "GET"
	}
	timeout := time.Duration(cfg.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 2500 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var body io.Reader
	if method == "POST" && cfg.Body != "" {
		body = bytes.NewBufferString(cfg.Body)
	}

	req, err := http.NewRequestWithContext(ctx, method, cfg.URL, body)
	if err != nil {
		return nil, err
	}

	for k, v := range cfg.Headers {
		req.Header.Set(k, v)
	}
	if method == "POST" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, string(b))
	}

	var root any
	if err := json.Unmarshal(b, &root); err != nil {
		return nil, fmt.Errorf("json parse: %w", err)
	}

	heightAny, err := getByPath(root, cfg.HeightPath)
	if err != nil {
		return nil, fmt.Errorf("heightPath: %w", err)
	}
	hashAny, err := getByPath(root, cfg.HashPath)
	if err != nil {
		return nil, fmt.Errorf("hashPath: %w", err)
	}
	timeAny, err := getByPath(root, cfg.TimePath)
	if err != nil {
		return nil, fmt.Errorf("timePath: %w", err)
	}

	height := toString(heightAny)
	hash := toString(hashAny)
	tunix := toUnixSeconds(timeAny, cfg.TimeUnit)

	if height == "" || hash == "" || tunix <= 0 {
		return nil, fmt.Errorf("invalid parsed fields height=%q hash=%q time=%d", height, hash, tunix)
	}

	return &block.Block{
		Height:  height,
		Hash:    hash,
		TimeUnix: tunix,
		SourceID: cfg.ID,
	}, nil
}

func toString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		// JSON number
		// Tron height 通常是整数
		return fmt.Sprintf("%.0f", x)
	case int:
		return fmt.Sprintf("%d", x)
	case int64:
		return fmt.Sprintf("%d", x)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func toUnixSeconds(v any, unit string) int64 {
	// JSON number 以 float64 常见
	var n int64
	switch x := v.(type) {
	case float64:
		n = int64(x)
	case int64:
		n = x
	case int:
		n = int64(x)
	case string:
		// 允许字符串时间戳
		// 只取数字
		var t int64
		for i := 0; i < len(x); i++ {
			if x[i] < '0' || x[i] > '9' {
				return 0
			}
			t = t*10 + int64(x[i]-'0')
		}
		n = t
	default:
		return 0
	}

	if unit == "ms" {
		return n / 1000
	}
	// 默认秒
	return n
}
