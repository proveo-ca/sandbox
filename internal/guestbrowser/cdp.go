// SPEC: _spec/_plans/isolated-browser-profiles.puml
package guestbrowser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type service struct {
	ctx       context.Context
	token     string
	agentHost string
	browser   browser
	control   controls
}

type controls struct {
	mu      sync.Mutex
	pending int
	idle    chan struct{}
	owner   string
	expires time.Time
}

func (c *controls) expire() {
	if time.Now().After(c.expires) {
		c.owner = ""
	}
}

func (c *controls) start() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expire()
	if c.owner != "" {
		return false
	}
	if c.pending == 0 {
		c.idle = make(chan struct{})
	}
	c.pending++
	return true
}

func (c *controls) complete(n int) {
	if n == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending -= n
	if c.pending == 0 {
		close(c.idle)
	}
}

func (c *controls) take(ctx context.Context, owner string) bool {
	c.mu.Lock()
	c.expire()
	if c.owner != "" && c.owner != owner {
		c.mu.Unlock()
		return false
	}
	c.owner, c.expires = owner, time.Now().Add(time.Minute)
	idle, pending := c.idle, c.pending
	c.mu.Unlock()
	if pending > 0 {
		select {
		case <-idle:
		case <-ctx.Done():
			c.release(owner)
			return false
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.owner == owner
}

func (c *controls) release(owner string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.owner == owner {
		c.owner = ""
	}
}

func (c *controls) input(owner string, f func() error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expire()
	if c.owner != owner || c.pending != 0 {
		return fmt.Errorf("take control first")
	}
	c.expires = time.Now().Add(time.Minute)
	return f()
}

type message struct {
	ID      json.RawMessage `json:"id,omitempty"`
	Session string          `json:"sessionId,omitempty"`
	Method  string          `json:"method,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type target struct {
	ID    string `json:"targetId"`
	Type  string `json:"type"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

func (s *service) agent(w http.ResponseWriter, r *http.Request) {
	if r.Host != s.agentHost || r.Header.Get("Origin") != "" {
		http.Error(w, "guest-local CDP only", http.StatusForbidden)
		return
	}
	ws, err := s.browser.endpoint(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	u, err := url.Parse(ws)
	if err != nil {
		http.Error(w, "invalid Chromium endpoint", http.StatusBadGateway)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/json/version":
		writeJSON(w, map[string]string{"Browser": "Chrome", "Protocol-Version": "1.3", "webSocketDebuggerUrl": "ws://" + s.agentHost + u.Path})
	case r.Method == http.MethodGet && (r.URL.Path == "/json/list" || r.URL.Path == "/json"):
		tabs, err := s.tabs(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		list := make([]map[string]string, 0, len(tabs))
		for _, tab := range tabs {
			list = append(list, map[string]string{"id": tab.ID, "type": tab.Type, "title": tab.Title,
				"url": tab.URL, "webSocketDebuggerUrl": "ws://" + s.agentHost + "/devtools/page/" + tab.ID})
		}
		writeJSON(w, list)
	case r.URL.Path == u.Path || strings.HasPrefix(r.URL.Path, "/devtools/page/"):
		s.relay(w, r, "ws://"+u.Host+r.URL.Path)
	default:
		http.NotFound(w, r)
	}
}

var cdpDialer = websocket.Dialer{HandshakeTimeout: 5 * time.Second}

func (s *service) relay(w http.ResponseWriter, r *http.Request, endpoint string) {
	up, _, err := cdpDialer.DialContext(r.Context(), endpoint, nil)
	if err != nil {
		http.Error(w, "Chromium WebSocket unavailable", http.StatusBadGateway)
		return
	}
	defer func() { _ = up.Close() }()
	upgrader := websocket.Upgrader{}
	down, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = down.Close() }()
	defer context.AfterFunc(s.ctx, func() { _ = down.Close(); _ = up.Close() })()
	var state sync.Mutex
	pending := map[string]bool{}
	defer func() {
		state.Lock()
		s.control.complete(len(pending))
		clear(pending)
		state.Unlock()
	}()
	var write sync.Mutex
	reply := func(data []byte) error {
		write.Lock()
		defer write.Unlock()
		_ = down.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return down.WriteMessage(websocket.TextMessage, data)
	}
	received := make(chan struct{})
	go func() {
		defer close(received)
		defer func() { _ = down.Close() }()
		for {
			_, data, err := up.ReadMessage()
			if err != nil {
				return
			}
			var msg message
			if json.Unmarshal(data, &msg) == nil && len(msg.ID) > 0 {
				state.Lock()
				key := msg.Session + ":" + string(msg.ID)
				if pending[key] {
					delete(pending, key)
					s.control.complete(1)
				}
				state.Unlock()
			}
			if reply(data) != nil {
				return
			}
		}
	}()
	defer func() { _ = up.Close(); _ = down.Close(); <-received }()
	down.SetReadLimit(16 << 20)
	for {
		kind, data, err := down.ReadMessage()
		var msg message
		if err != nil || kind != websocket.TextMessage || json.Unmarshal(data, &msg) != nil || len(msg.ID) == 0 || msg.Method == "" {
			return
		}
		state.Lock()
		key := msg.Session + ":" + string(msg.ID)
		if pending[key] {
			state.Unlock()
			return
		}
		if !s.control.start() {
			state.Unlock()
			refused, _ := json.Marshal(map[string]any{"id": msg.ID, "error": map[string]any{
				"code": -32000, "message": "Operator has browser control. Retry after control is returned.",
			}, "sessionId": omitEmpty(msg.Session)})
			if reply(refused) != nil {
				return
			}
			continue
		}
		pending[key] = true
		state.Unlock()
		_ = up.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if err := up.WriteMessage(kind, data); err != nil {
			return
		}
	}
}

func omitEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *service) tabs(ctx context.Context) ([]target, error) {
	ws, err := s.browser.endpoint(ctx)
	if err != nil {
		return nil, err
	}
	var result struct{ TargetInfos []target }
	if err := command(ctx, ws, "Target.getTargets", nil, &result); err != nil {
		return nil, err
	}
	tabs := make([]target, 0, len(result.TargetInfos))
	for _, tab := range result.TargetInfos {
		if tab.Type == "page" {
			tabs = append(tabs, tab)
		}
	}
	return tabs, nil
}

func command(ctx context.Context, endpoint, method string, params, result any) error {
	c, _, err := cdpDialer.DialContext(ctx, endpoint, nil)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	defer context.AfterFunc(ctx, func() { _ = c.Close() })()
	deadline := time.Now().Add(8 * time.Second)
	_ = c.SetReadDeadline(deadline)
	_ = c.SetWriteDeadline(deadline)
	if err := c.WriteJSON(map[string]any{"id": 1, "method": method, "params": params}); err != nil {
		return err
	}
	for {
		var msg message
		if err := c.ReadJSON(&msg); err != nil {
			return err
		}
		if string(msg.ID) != "1" {
			continue
		}
		if msg.Error != nil {
			return fmt.Errorf("%s: %s", method, msg.Error.Message)
		}
		if result != nil {
			return json.Unmarshal(msg.Result, result)
		}
		return nil
	}
}
