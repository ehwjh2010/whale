package acp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/usewhale/whale/internal/agent"
	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/policy"
	"github.com/usewhale/whale/internal/session"
	"github.com/usewhale/whale/internal/store"
	"github.com/usewhale/whale/internal/tools"
)

type Handler struct {
	transport       *Transport
	agent           *agent.Agent
	store           store.MessageStore
	dataDir         string
	defaultCwd      string
	toolset         *tools.Toolset
	activeSessionID *string
	policy          policy.RulePolicy
	metaDir         string

	promptMu     sync.Mutex
	policyLoader func(cwd string) policy.RulePolicy
	mu           sync.Mutex
	sessions     map[string]*sessionContext
}

// sessionMeta is the persisted, cross-restart state for a session. Messages
// live in the message store; this sidecar captures the context that ACP's
// session/load request does not carry (cwd, mode).
type sessionMeta struct {
	Cwd  string       `json:"cwd,omitempty"`
	Mode session.Mode `json:"mode,omitempty"`
}

type sessionContext struct {
	whaleSessionID string
	cancel         context.CancelFunc   // active prompt's cancel
	pendingCancels []context.CancelFunc // queued prompt cancels (LIFO order)
	cwd            string
	mode           session.Mode
}

func NewHandler(transport *Transport, whaleAgent *agent.Agent, msgStore store.MessageStore, defaultCwd string) *Handler {
	return &Handler{
		transport:  transport,
		agent:      whaleAgent,
		store:      msgStore,
		defaultCwd: defaultCwd,
		sessions:   make(map[string]*sessionContext),
	}
}

func (h *Handler) SetToolset(ts *tools.Toolset) { h.toolset = ts }

func (h *Handler) SetDataDir(dir string) { h.dataDir = dir }

func (h *Handler) SetPolicyLoader(fn func(cwd string) policy.RulePolicy) { h.policyLoader = fn }

func (h *Handler) SetPolicy(p policy.RulePolicy) { h.policy = p }

func (h *Handler) SetSessionIDProvider(sid *string) { h.activeSessionID = sid }

// SetSessionsDir sets the directory used to persist per-session metadata
// (cwd, mode) so it survives across process restarts and session/load.
func (h *Handler) SetSessionsDir(dir string) { h.metaDir = dir }

func (h *Handler) metaPath(sessionID string) string {
	if h.metaDir == "" {
		return ""
	}
	return filepath.Join(h.metaDir, sessionID+".meta.json")
}

// saveSessionMeta persists a session's cwd and mode. Failures are logged and
// otherwise ignored — metadata is best-effort and never blocks a request.
func (h *Handler) saveSessionMeta(sessionID string, meta sessionMeta) {
	path := h.metaPath(sessionID)
	if path == "" {
		return
	}
	b, err := json.Marshal(meta)
	if err != nil {
		Logger.Printf("failed to marshal session meta for %s: %v", sessionID, err)
		return
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		Logger.Printf("failed to write session meta for %s: %v", sessionID, err)
	}
}

// loadSessionMeta reads persisted metadata for a session. Returns ok=false if
// no metadata exists (or it cannot be read/parsed).
func (h *Handler) loadSessionMeta(sessionID string) (sessionMeta, bool) {
	path := h.metaPath(sessionID)
	if path == "" {
		return sessionMeta{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return sessionMeta{}, false
	}
	var meta sessionMeta
	if err := json.Unmarshal(b, &meta); err != nil {
		Logger.Printf("failed to parse session meta for %s: %v", sessionID, err)
		return sessionMeta{}, false
	}
	return meta, true
}

func (h *Handler) Run() error {
	h.transport.StartDispatcher()
	requests := h.transport.Requests()
	notifications := h.transport.Notifications()
	done := h.transport.Done()

	for {
		select {
		case item, ok := <-requests:
			if !ok {
				return nil
			}
			if item.Req.Method == MethodSessionPrompt {
				go h.handlePromptAsync(item)
			} else {
				h.handleRequest(item.Req)
			}
		case raw, ok := <-notifications:
			if !ok {
				return nil
			}
			h.handleNotificationRaw(raw)
		case <-done:
			return nil
		}
	}
}

func (h *Handler) handlePromptAsync(item *dispatchItem) {
	if errResp := h.handlePrompt(item.Req); errResp != nil {
		h.transport.SendError(errResp)
	}
}

func (h *Handler) handleNotificationRaw(raw json.RawMessage) {
	method := ExtractMethod(raw)
	params := ExtractParams(raw)
	switch method {
	case MethodSessionCancel:
		var p struct {
			SessionID string `json:"sessionId"`
		}
		json.Unmarshal(params, &p)
		if p.SessionID == "" {
			return
		}
		h.mu.Lock()
		sctx, ok := h.sessions[p.SessionID]
		var cancels []context.CancelFunc
		if ok {
			if sctx.cancel != nil {
				cancels = append(cancels, sctx.cancel)
			}
			if sctx.pendingCancels != nil {
				cancels = append(cancels, sctx.pendingCancels...)
			}
		}
		h.mu.Unlock()
		for _, fn := range cancels {
			fn()
		}
		if len(cancels) > 0 {
			h.transport.TriggerCancel()
			Logger.Printf("session cancelled (via notification): %s", p.SessionID)
		}
	default:
		Logger.Printf("ignoring unknown notification: %s", method)
	}
}

func (h *Handler) handleRequest(req *RPCRequest) {
	var errResp *RPCErrorResponse
	switch req.Method {
	case MethodInitialize:
		errResp = h.handleInitialize(req)
	case MethodAuthenticate:
		errResp = h.handleAuthenticate(req)
	case MethodSessionNew:
		errResp = h.handleSessionNew(req)
	case MethodSessionLoad:
		errResp = h.handleSessionLoad(req)
	case MethodSessionSetMode:
		errResp = h.handleSetMode(req)
	case MethodSessionCancel:
		errResp = h.handleCancel(req)
	default:
		errResp = NewErrorResponse(req.ID, ErrCodeMethodNotFound, fmt.Sprintf("method not found: %s", req.Method))
	}
	if errResp != nil {
		h.transport.SendError(errResp)
	}
}

func (h *Handler) handleInitialize(req *RPCRequest) *RPCErrorResponse {
	var params InitializeRequest
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, fmt.Sprintf("invalid params: %v", err))
	}
	if params.ClientCapabilities != nil {
		Logger.Printf("client capabilities: fs.read=%v fs.write=%v terminal=%v",
			params.ClientCapabilities.FS != nil && params.ClientCapabilities.FS.ReadTextFile,
			params.ClientCapabilities.FS != nil && params.ClientCapabilities.FS.WriteTextFile,
			params.ClientCapabilities.Terminal)
	}
	h.transport.SendResponse(NewSuccessResponse(req.ID, InitializeResponse{
		ProtocolVersion: ProtocolVersion,
		AgentCapabilities: &AgentCapabilities{
			LoadSession: true,
			PromptCapabilities: &PromptCapabilities{
				Image: false, Audio: false, EmbeddedContext: true,
			},
			MCPCapabilities: &MCPCapabilities{HTTP: false, SSE: false},
		},
		AgentInfo: &Implementation{Name: "whale", Title: "Whale", Version: "0.1.0"},
	}))
	return nil
}

func (h *Handler) handleAuthenticate(req *RPCRequest) *RPCErrorResponse {
	h.transport.SendResponse(NewSuccessResponse(req.ID, AuthenticateResponse{}))
	return nil
}

func (h *Handler) handleSessionNew(req *RPCRequest) *RPCErrorResponse {
	var params NewSessionRequest
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, fmt.Sprintf("invalid params: %v", err))
	}
	whaleSessionID := newSessionID()
	cwd := params.Cwd
	if cwd == "" {
		cwd = h.defaultCwd
	}
	h.mu.Lock()
	h.sessions[whaleSessionID] = &sessionContext{whaleSessionID: whaleSessionID, cwd: cwd, mode: session.ModeAgent}
	h.mu.Unlock()
	h.saveSessionMeta(whaleSessionID, sessionMeta{Cwd: cwd, Mode: session.ModeAgent})
	Logger.Printf("new session: acp=%s cwd=%s", whaleSessionID, cwd)
	h.transport.SendResponse(NewSuccessResponse(req.ID, NewSessionResponse{
		SessionID: whaleSessionID,
		Modes: &SessionModeState{
			CurrentModeID: "code",
			AvailableModes: []SessionMode{
				{ID: "ask", Name: "Ask", Description: "Read-only Q&A without making changes"},
				{ID: "architect", Name: "Architect", Description: "Design and plan without implementation"},
				{ID: "code", Name: "Code", Description: "Full agent with tool access"},
			},
		},
	}))
	return nil
}

func (h *Handler) handleSessionLoad(req *RPCRequest) *RPCErrorResponse {
	var params LoadSessionRequest
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, fmt.Sprintf("invalid params: %v", err))
	}
	messages, err := h.store.List(context.Background(), params.SessionID)
	if err != nil {
		Logger.Printf("failed to load messages for session %s: %v", params.SessionID, err)
		messages = nil
	}
	// Restore the persisted cwd/mode so a reloaded session operates against its
	// original workspace rather than the process default. session/load does not
	// carry cwd, so this sidecar is the only source of truth.
	cwd := h.defaultCwd
	mode := session.ModeAgent
	if meta, ok := h.loadSessionMeta(params.SessionID); ok {
		if meta.Cwd != "" {
			cwd = meta.Cwd
		}
		if meta.Mode != "" {
			mode = meta.Mode
		}
	}
	h.mu.Lock()
	if _, exists := h.sessions[params.SessionID]; !exists {
		h.sessions[params.SessionID] = &sessionContext{whaleSessionID: params.SessionID, cwd: cwd, mode: mode}
	}
	h.mu.Unlock()
	for _, msg := range messages {
		if update := h.translateMessage(msg); update != nil {
			h.transport.SendNotification(MethodSessionUpdate, SessionNotification{
				SessionID: params.SessionID, Update: *update,
			})
		}
	}
	Logger.Printf("session loaded: %s (%d messages replayed)", params.SessionID, len(messages))
	currentMode := "code"
	h.mu.Lock()
	if sctx, ok := h.sessions[params.SessionID]; ok {
		switch sctx.mode {
		case session.ModeAsk:
			currentMode = "ask"
		case session.ModePlan:
			currentMode = "architect"
		}
	}
	h.mu.Unlock()
	h.transport.SendResponse(NewSuccessResponse(req.ID, LoadSessionResponse{
		Modes: &SessionModeState{
			CurrentModeID: currentMode,
			AvailableModes: []SessionMode{
				{ID: "ask", Name: "Ask", Description: "Read-only Q&A without making changes"},
				{ID: "architect", Name: "Architect", Description: "Design and plan without implementation"},
				{ID: "code", Name: "Code", Description: "Full agent with tool access"},
			},
		},
	}))
	return nil
}

var acpToWhaleMode = map[string]session.Mode{
	"code": session.ModeAgent, "ask": session.ModeAsk, "architect": session.ModePlan,
}

func (h *Handler) handleSetMode(req *RPCRequest) *RPCErrorResponse {
	var params SetSessionModeRequest
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, fmt.Sprintf("invalid params: %v", err))
	}
	h.mu.Lock()
	sctx, ok := h.sessions[params.SessionID]
	var savedCwd string
	var savedMode session.Mode
	if ok {
		wm, found := acpToWhaleMode[params.ModeID]
		if !found {
			h.mu.Unlock()
			return NewErrorResponse(req.ID, ErrCodeInvalidParams, fmt.Sprintf("unknown mode: %s", params.ModeID))
		}
		sctx.mode = wm
		savedCwd = sctx.cwd
		savedMode = wm
		Logger.Printf("mode change: session=%s mode=%s", params.SessionID, wm)
	}
	h.mu.Unlock()
	if !ok {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, fmt.Sprintf("session not found: %s", params.SessionID))
	}
	h.saveSessionMeta(params.SessionID, sessionMeta{Cwd: savedCwd, Mode: savedMode})
	h.transport.SendResponse(NewSuccessResponse(req.ID, SetSessionModeResponse{}))
	return nil
}

func (h *Handler) handleCancel(req *RPCRequest) *RPCErrorResponse {
	var params struct {
		SessionID string `json:"sessionId"`
	}
	json.Unmarshal(req.Params, &params)
	h.mu.Lock()
	sctx, ok := h.sessions[params.SessionID]
	var cancels []context.CancelFunc
	if ok {
		if sctx.cancel != nil {
			cancels = append(cancels, sctx.cancel)
		}
		if sctx.pendingCancels != nil {
			cancels = append(cancels, sctx.pendingCancels...)
		}
	}
	h.mu.Unlock()
	for _, fn := range cancels {
		fn()
	}
	if len(cancels) > 0 {
		h.transport.TriggerCancel()
		Logger.Printf("session cancelled: %s", params.SessionID)
	}
	if req.IsNotification() {
		return nil
	}
	h.transport.SendResponse(NewSuccessResponse(req.ID, struct{}{}))
	return nil
}

func (h *Handler) translateMessage(msg core.Message) *SessionUpdate {
	switch msg.Role {
	case core.RoleUser:
		if msg.Hidden {
			return nil
		}
		cb := TextBlock(msg.Text)
		return &SessionUpdate{SessionUpdate: "user_message_chunk", Content: &cb}
	case core.RoleAssistant:
		if msg.Text != "" {
			cb := TextBlock(msg.Text)
			return &SessionUpdate{SessionUpdate: "agent_message_chunk", Content: &cb}
		}
	}
	return nil
}

func newSessionID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return "acp-" + hex.EncodeToString(b)
}
