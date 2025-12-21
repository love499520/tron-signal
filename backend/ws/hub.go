package ws

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

// Hub
// - 仅用于服务端广播信号
// - 不做任何业务判断
// - 不承载 UI 状态（UI 用 SSE）
type Hub struct {
	mu      sync.RWMutex
	clients map[*websocket.Conn]struct{}
}

func NewHub() *Hub {
	return &Hub{
		clients: make(map[*websocket.Conn]struct{}),
	}
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		// 鉴权已在 HTTP 层完成，这里不再校验
		return true
	},
}

// HandleWS
// 仅负责建立连接 + 注册 client
func (h *Hub) HandleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WS_UPGRADE_ERROR: %v\n", err)
		return
	}

	h.mu.Lock()
	h.clients[conn] = struct{}{}
	h.mu.Unlock()

	log.Printf("WS_CONNECTED remote=%s\n", r.RemoteAddr)

	// 阻塞读，直到断开
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}

	h.mu.Lock()
	delete(h.clients, conn)
	h.mu.Unlock()

	_ = conn.Close()
	log.Printf("WS_DISCONNECTED remote=%s\n", r.RemoteAddr)
}

// BroadcastSignal
// Core 调用此方法
func (h *Hub) BroadcastSignal(payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("WS_MARSHAL_ERROR: %v\n", err)
		return
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for c := range h.clients {
		if err := c.WriteMessage(websocket.TextMessage, data); err != nil {
			// 写失败直接移除
			_ = c.Close()
			delete(h.clients, c)
		}
	}
}
