// SPEC: _spec/_plans/isolated-browser-profiles.puml
package guestbrowser

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

//go:embed viewport.html viewport.js
var assets embed.FS

func (s *service) viewport(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(TokenHeader)), []byte(s.token)) != 1 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'unsafe-inline'; img-src 'self' blob:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'")
	if r.Method == http.MethodGet {
		s.view(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.act(w, r)
}

func (s *service) view(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/health":
		w.WriteHeader(http.StatusNoContent)
	case "/", "/viewport.js":
		name, contentType := "viewport.html", "text/html; charset=utf-8"
		if r.URL.Path == "/viewport.js" {
			name, contentType = "viewport.js", "text/javascript; charset=utf-8"
		}
		b, _ := assets.ReadFile(name)
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(b)
	case "/tabs":
		tabs, err := s.tabs(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, tabs)
	case "/frame":
		var frame struct{ Data string }
		err := s.page(r.Context(), r.URL.Query().Get("tab"), "Page.captureScreenshot", map[string]any{
			"format": "jpeg", "quality": 70, "captureBeyondViewport": false,
		}, &frame)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		b, err := base64.StdEncoding.DecodeString(frame.Data)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(b)
	default:
		http.NotFound(w, r)
	}
}

func (s *service) page(ctx context.Context, tab, method string, params, result any) error {
	ws, err := s.browser.endpoint(ctx)
	if err != nil {
		return err
	}
	if len(tab) != 32 || strings.IndexFunc(strings.ToUpper(tab), func(r rune) bool {
		return !strings.ContainsRune("0123456789ABCDEF", r)
	}) >= 0 {
		return fmt.Errorf("invalid browser tab")
	}
	base, _, _ := strings.Cut(ws, "/devtools/")
	return command(ctx, base+"/devtools/page/"+tab, method, params, result)
}

func (s *service) act(w http.ResponseWriter, r *http.Request) {
	owner := r.Header.Get("X-Viewport-Client")
	if len(owner) != 36 {
		http.Error(w, "missing viewport identity", http.StatusBadRequest)
		return
	}
	var err error
	switch r.URL.Path {
	case "/take":
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		if !s.control.take(ctx, owner) {
			err = fmt.Errorf("browser is busy or another viewport has control")
		}
	case "/return":
		s.control.release(owner)
	case "/hold":
		err = s.control.input(owner, func() error { return nil })
	case "/input":
		var input operatorInput
		if err := readJSON(r, &input); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		err = s.control.input(owner, func() error { return input.send(r.Context(), s) })
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type operatorInput struct {
	Tab, Kind, Key, Code, Button, Text, URL string
	X, Y, DeltaX, DeltaY                    float64
	Modifiers, KeyCode, ClickCount          int
}

func (i operatorInput) send(ctx context.Context, s *service) error {
	page := func(method string, params map[string]any) error {
		return s.page(ctx, i.Tab, method, params, nil)
	}
	switch i.Kind {
	case "mousePressed", "mouseReleased", "mouseMoved", "mouseWheel":
		p := map[string]any{"type": i.Kind, "x": i.X, "y": i.Y, "modifiers": i.Modifiers, "button": i.Button, "clickCount": i.ClickCount}
		if i.Kind == "mouseWheel" {
			p["deltaX"], p["deltaY"] = i.DeltaX, i.DeltaY
		}
		return page("Input.dispatchMouseEvent", p)
	case "keyDown", "keyUp":
		return page("Input.dispatchKeyEvent", map[string]any{
			"type": i.Kind, "key": i.Key, "code": i.Code, "windowsVirtualKeyCode": i.KeyCode, "modifiers": i.Modifiers, "text": i.Text,
		})
	case "text":
		return page("Input.insertText", map[string]any{"text": i.Text})
	case "navigate", "new":
		u, err := url.Parse(i.URL)
		if err != nil || (i.URL != "about:blank" && (u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http"))) {
			return fmt.Errorf("enter an HTTP or HTTPS address")
		}
		p := map[string]any{"url": i.URL}
		if i.Kind == "navigate" {
			return page("Page.navigate", p)
		}
		ws, err := s.browser.endpoint(ctx)
		if err != nil {
			return err
		}
		return command(ctx, ws, "Target.createTarget", p, nil)
	default:
		return fmt.Errorf("unknown browser input")
	}
}
