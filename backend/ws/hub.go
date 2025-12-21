package ws

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Hub：WS 广播中心（仅广播信号，不做任何业务判断）
type Hub struct {
	mu      sync.RWMutex
	clients map[*websocket.Conn]struct{}

	upgrader websocket.Upgrader
}

func NewHub() *Hub {
	return &Hub{
		clients: map[*websocket.Conn]struct{}{},
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

// Handle：升级 WS 并加入广播集合
func (h *Hub) Handle(w http.ResponseWriter, r *http.Request) {
	c, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WS_UPGRADE_ERR: %v\n", err)
		return
	}

	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()

	// 读循环：只为了探测断连（1003：断连必须记录日志）
	go func() {
		defer func() {
			h.mu.Lock()
			delete(h.clients, c)
			h.mu.Unlock()
			_ = c.Close()
			log.Printf("WS_DISCONNECT\n")
		}()

		_ = c.SetReadDeadline(time.Time{}) // 不依赖 ping/pong
		for {
			_, _, err := c.ReadMessage()
			if err != nil {
				return
			}
		}
	}()
}

// Broadcast：把信号 JSON 广播给所有连接
func (h *Hub) Broadcast(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}

	h.mu.RLock()
	clients := make([]*websocket.Conn, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.RUnlock()

	for _, c := range clients {
		_ = c.SetWriteDeadline(time.Now().Add(2 * time.Second))
		if err := c.WriteMessage(websocket.TextMessage, b); err != nil {
			// 写失败视为断开，移除
			h.mu.Lock()
			delete(h.clients, c)
			h.mu.Unlock()
			_ = c.Close()
			log.Printf("WS_WRITE_ERR: %v\n", err)
		}
	}
}

// Count：用于 UI 显示连接数（可选）
func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}
