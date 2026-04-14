package server

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"

	"github.com/dakshjotwani/gru/internal/store"
)

var wsUpgrader = websocket.Upgrader{
	// Origin check is skipped here; callers must authenticate via token query param.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// TerminalHandler streams a tmux pane over WebSocket using a PTY.
//
// Auth: browsers cannot set custom headers on WebSocket upgrades, so we accept
// the API key via the ?token= query parameter instead of Authorization header.
//
// Protocol (after upgrade):
//   - Server → client: binary frames containing raw PTY output bytes.
//   - Client → server: binary frames containing raw keystrokes / stdin bytes.
//   - Client → server: text frames containing JSON resize events:
//     {"type":"resize","cols":N,"rows":N}
type TerminalHandler struct {
	apiKey string
	store  *store.Store
}

func NewTerminalHandler(apiKey string, s *store.Store) http.Handler {
	return &TerminalHandler{apiKey: apiKey, store: s}
}

func (h *TerminalHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Auth via query param.
	token := r.URL.Query().Get("token")
	if subtle.ConstantTimeCompare([]byte(token), []byte(h.apiKey)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	sessionID := r.PathValue("session_id")
	if sessionID == "" {
		http.Error(w, "missing session_id", http.StatusBadRequest)
		return
	}

	row, err := h.store.Queries().GetSession(r.Context(), sessionID)
	if err != nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	if row.TmuxSession == nil || *row.TmuxSession == "" {
		http.Error(w, "session has no tmux target", http.StatusBadRequest)
		return
	}

	target := *row.TmuxSession
	if row.TmuxWindow != nil && *row.TmuxWindow != "" {
		target = fmt.Sprintf("%s:%s", target, *row.TmuxWindow)
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("terminal: ws upgrade %s: %v", sessionID, err)
		return
	}
	defer conn.Close()

	// Spawn tmux attach in a PTY so we get a proper terminal with ANSI output.
	cmd := exec.Command("tmux", "attach-session", "-t", target)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		log.Printf("terminal: pty start %s: %v", target, err)
		errMsg := fmt.Sprintf("failed to attach to %s: %v\r\n", target, err)
		_ = conn.WriteMessage(websocket.BinaryMessage, []byte(errMsg))
		return
	}
	defer func() {
		_ = ptmx.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	// PTY → WebSocket: relay raw terminal output as binary frames.
	ptmxDone := make(chan struct{})
	go func() {
		defer close(ptmxDone)
		buf := make([]byte, 4096)
		for {
			n, readErr := ptmx.Read(buf)
			if n > 0 {
				if writeErr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); writeErr != nil {
					return
				}
			}
			if readErr != nil {
				if readErr != io.EOF {
					log.Printf("terminal: pty read %s: %v", target, readErr)
				}
				return
			}
		}
	}()

	// WebSocket → PTY: binary = stdin, text = JSON control (resize).
	type resizeMsg struct {
		Type string `json:"type"`
		Cols uint16 `json:"cols"`
		Rows uint16 `json:"rows"`
	}
	for {
		msgType, msg, readErr := conn.ReadMessage()
		if readErr != nil {
			break
		}
		switch msgType {
		case websocket.BinaryMessage:
			if _, writeErr := ptmx.Write(msg); writeErr != nil {
				log.Printf("terminal: pty write %s: %v", target, writeErr)
			}
		case websocket.TextMessage:
			var ctrl resizeMsg
			if jsonErr := json.Unmarshal(msg, &ctrl); jsonErr == nil &&
				ctrl.Type == "resize" && ctrl.Cols > 0 && ctrl.Rows > 0 {
				if sizeErr := pty.Setsize(ptmx, &pty.Winsize{Cols: ctrl.Cols, Rows: ctrl.Rows}); sizeErr != nil {
					log.Printf("terminal: resize %s: %v", target, sizeErr)
				}
			}
		}
	}

	<-ptmxDone
}
