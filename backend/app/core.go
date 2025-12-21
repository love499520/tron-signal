package app

import (
	"errors"
	"sync"
	"time"

	"tron-signal/backend/config"
	"tron-signal/backend/judge"
	"tron-signal/backend/logs"
	"tron-signal/backend/poller"
)

type Core struct {
	mu sync.Mutex

	store *config.Store
	log   *logs.Logger

	judge judge.Engine

	// runtime
	listening   bool
	lastHeight  int64
	lastHash    string
	lastTimeISO string

	reconnects int

	// blocks cache (latest 50)
	blocks []BlockView

	// machines & sources are stored in config; runtime compiled state:
	machines *MachineManager
	poller   *poller.Manager
	wsHub    *WSHub
}

type BlockView struct {
	Time   string `json:"time"`   // YYYY/MM/DD HH:mm:ss
	Height int64  `json:"height"` // block height
	Hash   string `json:"hash"`   // full hash (UI can format)
	State  string `json:"state"`  // ON/OFF
}

func NewCore(store *config.Store, log *logs.Logger) *Core {
	c := &Core{
		store:  store,
		log:    log,
		blocks: make([]BlockView, 0, 50),
		wsHub:  NewWSHub(),
	}
	c.reloadFromConfigLocked()
	return c
}

func (c *Core) reloadFromConfigLocked() {
	cfg := c.store.Get()

	// judge engine
	c.judge = judge.NewEngine(cfg.JudgeRule)

	// machines
	c.machines = NewMachineManager(cfg.Machines, c.log)

	// poller manager
	c.poller = poller.NewManager(cfg.Sources, c.log)
}

func (c *Core) Start() {
	c.mu.Lock()
	c.listening = true
	c.mu.Unlock()

	// polling loop
	go c.poller.Run(func(b poller.Block) {
		c.onBlock(b)
	})
}

func (c *Core) Stop() {
	c.mu.Lock()
	c.listening = false
	c.mu.Unlock()
	if c.poller != nil {
		c.poller.Stop()
	}
}

func (c *Core) GetStatus() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return map[string]any{
		"Listening":     c.listening,
		"LastHeight":    c.lastHeight,
		"LastHash":      c.lastHash,
		"LastTimeISO":   c.lastTimeISO,
		"Reconnects":    c.reconnects,
		"JudgeRule":     c.store.Get().JudgeRule,
		"MachinesCount": c.machines.Count(),
		"SourcesCount":  len(c.store.Get().Sources),
	}
}

func (c *Core) GetMachines() []any {
	cfg := c.store.Get()
	return cfg.Machines
}

func (c *Core) SaveMachines(body map[string]any) error {
	raw, ok := body["machines"]
	if !ok {
		// 兼容前端直接传 {machines:[...]}
		if arr, ok2 := body["Machines"]; ok2 {
			raw = arr
			ok = true
		}
	}
	if !ok {
		return errors.New("MISSING_MACHINES")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	cfg := c.store.Get()
	cfg.Machines, _ = raw.([]any)
	if cfg.Machines == nil {
		// 如果 json decode 出来是 []interface{}，这里 ok
		// 若是 map 结构，直接存也允许（后续 manager 解析再校验）
		if v, ok := raw.([]interface{}); ok {
			out := make([]any, 0, len(v))
			for _, it := range v {
				out = append(out, it)
			}
			cfg.Machines = out
		} else {
			return errors.New("BAD_MACHINES_TYPE")
		}
	}

	if err := c.store.Set(cfg); err != nil {
		return err
	}

	// reload runtime
	c.reloadFromConfigLocked()
	return nil
}

func (c *Core) DeleteMachine(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	cfg := c.store.Get()
	out := make([]any, 0, len(cfg.Machines))
	for _, it := range cfg.Machines {
		m, ok := it.(map[string]any)
		if !ok {
			out = append(out, it)
			continue
		}
		if s, ok := m["id"].(string); ok && s == id {
			continue
		}
		out = append(out, it)
	}
	cfg.Machines = out
	if err := c.store.Set(cfg); err != nil {
		return err
	}
	c.reloadFromConfigLocked()
	return nil
}

func (c *Core) GetSources() []config.Source {
	return c.store.Get().Sources
}

func (c *Core) UpsertSource(body map[string]any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	cfg := c.store.Get()

	// body should contain source object fields
	id, _ := body["id"].(string)
	if id == "" {
		return errors.New("MISSING_ID")
	}

	s := config.Source{
		ID:        id,
		Name:      str(body["name"]),
		Type:      str(body["type"]),
		Enabled:   boolv(body["enabled"]),
		BaseRate:  intv(body["baseRate"], 1),
		MaxRate:   intv(body["maxRate"], 1),
		Endpoint:  str(body["endpoint"]),
		APIKey:    str(body["apiKey"]),
		TimeoutMS: intv(body["timeoutMs"], 3500),
	}

	updated := false
	out := make([]config.Source, 0, len(cfg.Sources))
	for _, old := range cfg.Sources {
		if old.ID == s.ID {
			out = append(out, s)
			updated = true
		} else {
			out = append(out, old)
		}
	}
	if !updated {
		out = append(out, s)
	}
	cfg.Sources = out

	if err := c.store.Set(cfg); err != nil {
		return err
	}
	c.reloadFromConfigLocked()
	return nil
}

func (c *Core) DeleteSource(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	cfg := c.store.Get()
	out := make([]config.Source, 0, len(cfg.Sources))
	for _, s := range cfg.Sources {
		if s.ID == id {
			continue
		}
		out = append(out, s)
	}
	cfg.Sources = out
	if err := c.store.Set(cfg); err != nil {
		return err
	}
	c.reloadFromConfigLocked()
	return nil
}

func (c *Core) SwitchJudgeRule(rule string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	rule = normalizeRule(rule)
	if rule == "" {
		return errors.New("BAD_RULE")
	}

	// STOP ALL machines + reset runtime counters (must)
	c.machines.StopAllAndReset()

	cfg := c.store.Get()
	cfg.JudgeRule = rule
	if err := c.store.Set(cfg); err != nil {
		return err
	}

	c.reloadFromConfigLocked()
	c.log.Major("JUDGE_SWITCH", map[string]any{"rule": rule})
	return nil
}

func (c *Core) onBlock(b poller.Block) {
	state := c.judge.StateFromHash(b.Hash)

	// update status
	c.mu.Lock()
	c.lastHeight = b.Height
	c.lastHash = b.Hash
	c.lastTimeISO = time.Now().In(time.FixedZone("UTC+8", 8*3600)).Format(time.RFC3339)
	c.mu.Unlock()

	// push blocks list
	c.pushBlock(BlockView{
		Time:   b.TimeBJ,
		Height: b.Height,
		Hash:   b.Hash,
		State:  state,
	})

	// machines
	signals := c.machines.OnBlock(b.Height, b.Hash, state)

	// broadcast
	for _, s := range signals {
		c.wsHub.Broadcast(s)
	}
}

func (c *Core) pushBlock(v BlockView) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// insert at top
	c.blocks = append([]BlockView{v}, c.blocks...)
	if len(c.blocks) > 50 {
		c.blocks = c.blocks[:50]
	}
}

func (c *Core) GetBlocks() []BlockView {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]BlockView, 0, len(c.blocks))
	out = append(out, c.blocks...)
	return out
}

func (c *Core) ReadLogs(typ, source string, limit int) []map[string]any {
	return c.log.Read(typ, source, limit)
}

// ws
func (c *Core) AcceptBroadcastWS(w http.ResponseWriter, r *http.Request) error {
	return c.wsHub.Accept(w, r)
}

// helpers
func str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
func boolv(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}
func intv(v any, def int) int {
	switch t := v.(type) {
	case float64:
		if int(t) <= 0 {
			return def
		}
		return int(t)
	case int:
		if t <= 0 {
			return def
		}
		return t
	default:
		return def
	}
}
func normalizeRule(s string) string {
	switch s {
	case "lucky", "bigsmall", "oddeven":
		return s
	default:
		return ""
	}
}
