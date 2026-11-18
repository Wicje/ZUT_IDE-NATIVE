// Package opencode is a small client for a local opencode server, the
// agent users run on this machine. It reads sessions and providers, and
// sends prompts; the editor and the agents engine talk to the same server
// the "opencode" command line uses, on 127.0.0.1:4096 by default.
package opencode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// BaseURL is where the server listens: $OPENCODE_BASE_URL, else
// $ZUT_OPENCODE_URL, else http://127.0.0.1:4096.
func ok2xx(code int) bool { return code >= 200 && code < 300 }

func BaseURL() string {
	if v := strings.TrimSpace(os.Getenv("OPENCODE_BASE_URL")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("ZUT_OPENCODE_URL")); v != "" {
		return v
	}
	return "http://127.0.0.1:4096"
}

// Client talks to one opencode server. The zero value is not ready; use
// New.
type Client struct {
	base string
	hc   *http.Client
}

// New returns a client for the server at BaseURL.
func New() *Client {
	return &Client{
		base: BaseURL(),
		hc:   &http.Client{Timeout: 15 * time.Second},
	}
}

// Session is one conversation of the agent.
type Session struct {
	ID    string
	Dir   string
	Title string
	// Updated is when the session was last touched, zero when unknown.
	Updated time.Time
}

// Model is one model of a connected provider.
type Model struct {
	ProviderID string
	ID         string
	Name       string
	Free       bool
}

// Health pings the server: the error says it is not there.
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.base+"/global/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if !ok2xx(resp.StatusCode) {
		return fmt.Errorf("opencode server answered %d", resp.StatusCode)
	}
	return nil
}

// Sessions returns the sessions of this machine, the one used last first.
// roots selects only top-level sessions (not forks).
func (c *Client) Sessions(ctx context.Context, limit int) ([]Session, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/session?roots=true&limit=%d", c.base, limit), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("opencode server answered %d", resp.StatusCode)
	}
	var raw []struct {
		ID        string `json:"id"`
		Directory string `json:"directory"`
		Title     string `json:"title"`
		Time      *struct {
			Updated int64 `json:"updated"`
		} `json:"time"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	out := make([]Session, 0, len(raw))
	for _, s := range raw {
		sess := Session{ID: s.ID, Dir: s.Directory, Title: s.Title}
		if s.Time != nil && s.Time.Updated > 0 {
			sess.Updated = time.UnixMilli(s.Time.Updated)
		}
		out = append(out, sess)
	}
	// The server orders newest first already; keep that.
	return out, nil
}

// Models returns the models of the connected providers, free ones first.
func (c *Client) Models(ctx context.Context) ([]Model, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.base+"/provider", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("opencode server answered %d", resp.StatusCode)
	}
	var raw struct {
		All []struct {
			ID     string `json:"id"`
			Models map[string]struct {
				ID       string `json:"id"`
				Name     string `json:"name"`
				Provider string `json:"providerID"`
				Cost     *struct {
					Input  float64 `json:"input"`
					Output float64 `json:"output"`
				} `json:"cost"`
			} `json:"models"`
		} `json:"all"`
		Connected []string `json:"connected"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	connected := map[string]bool{}
	for _, id := range raw.Connected {
		connected[id] = true
	}
	out := []Model{}
	for _, p := range raw.All {
		for _, m := range p.Models {
			provider := m.Provider
			if provider == "" {
				provider = p.ID
			}
			if !connected[provider] {
				continue
			}
			mfree := m.Cost != nil && m.Cost.Input == 0 && m.Cost.Output == 0
			name := m.Name
			if name == "" {
				name = m.ID
			}
			out = append(out, Model{ProviderID: provider, ID: m.ID, Name: name, Free: mfree})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Free != out[j].Free {
			return out[i].Free
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// NewSession creates a session rooted at dir ("" for the server's cwd).
func (c *Client) NewSession(ctx context.Context, dir string) (string, error) {
	url := c.base + "/session"
	if dir != "" {
		url += "?directory=" + escape(dir)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(`{}`))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("opencode server answered %d", resp.StatusCode)
	}
	var raw struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return "", err
	}
	if raw.ID == "" {
		return "", errors.New("opencode server returned no session id")
	}
	return raw.ID, nil
}

// Prompt sends a prompt to a session without waiting for the reply; the
// server streams progress on the event bus (see Events).
func (c *Client) Prompt(ctx context.Context, sessionID, text, providerID, modelID string) error {
	type payload struct {
		Parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"parts"`
		Model *struct {
			ProviderID string `json:"providerID"`
			ModelID    string `json:"modelID"`
		} `json:"model,omitempty"`
	}
	body := payload{
		Parts: []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{{Type: "text", Text: text}},
	}
	if providerID != "" && modelID != "" {
		body.Model = &struct {
			ProviderID string `json:"providerID"`
			ModelID    string `json:"modelID"`
		}{providerID, modelID}
	}
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.base+"/session/"+sessionID+"/prompt_async", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if !ok2xx(resp.StatusCode) {
		return fmt.Errorf("opencode server answered %d", resp.StatusCode)
	}
	return nil
}

// Permission is a pending approval of a tool the agent wants to run.
type Permission struct {
	ID         string
	SessionID  string
	Permission string
	Patterns   []string
	Always     []string
}

// ReplyPermission answers a pending permission: "once", "always" or
// "reject".
func (c *Client) ReplyPermission(ctx context.Context, requestID, reply string) error {
	b, err := json.Marshal(map[string]string{"reply": reply})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.base+"/permission/"+requestID+"/reply", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if !ok2xx(resp.StatusCode) {
		return fmt.Errorf("opencode server answered %d", resp.StatusCode)
	}
	return nil
}

// Event is one thing the agent did, as the event bus carries it.
// Which fields are set depends on Type.
type Event struct {
	Type      string
	SessionID string

	// permission.asked / permission.replied (and their v2 forms).
	Permission *Permission
	RequestID  string
	Reply      string

	// message.updated: which message changed and who spoke.
	Role      string // "user" or "assistant"
	MessageID string

	// message.part.updated: the growing part of a message.
	PartID   string
	PartType string
	Text     string

	// tool parts: name and status of the tool call.
	Tool    string
	Status  string
	Title   string
	Command string

	// session.error
	ErrorMessage string
}

// Events streams the server's event bus until ctx is done; it is the
// caller's to drain. The channel is closed once the stream is gone.
// Any opencode server ≥1.x may emit events in either shape; both sets of
// names map onto the same Event here.
func (c *Client) Events(ctx context.Context, out chan<- Event) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.base+"/event", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	// No timeout on the stream: it is meant to be long-lived.
	hc := &http.Client{}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	if !ok2xx(resp.StatusCode) {
		resp.Body.Close()
		return fmt.Errorf("opencode server answered %d", resp.StatusCode)
	}
	go func() {
		defer close(out)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 1<<20), 8<<20)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "" {
				continue
			}
			var ev Event
			if !decodeEvent(payload, &ev) {
				continue
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return nil
}

type rawEvent struct {
	Type       string          `json:"type"`
	Properties json.RawMessage `json:"properties"`
}

type rawProps struct {
	SessionID  string          `json:"sessionID"`
	ID         string          `json:"id"`
	Permission string          `json:"permission"`
	Patterns   []string        `json:"patterns"`
	Always     []string        `json:"always"`
	Info       json.RawMessage `json:"info"`
	Part       json.RawMessage `json:"part"`
	Error      json.RawMessage `json:"error"`
	RequestID  string          `json:"requestID"`
	Reply      string          `json:"reply"`
	Action     string          `json:"action"`
	Resources  []string        `json:"resources"`
}

type rawMessageInfo struct {
	ID   string `json:"id"`
	Role string `json:"role"`
}

type rawPart struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	MessageID string          `json:"messageID"`
	Tool      string          `json:"tool"`
	CallID    string          `json:"callID"`
	Status    string          `json:"status"`
	Title     string          `json:"title"`
	Args      json.RawMessage `json:"args"`
	State     *struct {
		Status string          `json:"status"`
		Title  string          `json:"title"`
		Input  json.RawMessage `json:"input"`
	} `json:"state"`
}

type rawError struct {
	Name string `json:"name"`
	Data struct {
		Message string `json:"message"`
	} `json:"data"`
}

func decodeEvent(line string, ev *Event) bool {
	var raw rawEvent
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return false
	}
	ev.Type = raw.Type
	// Newer servers wrap the payload in a durable envelope:
	// {type, durable, location, data:{...}}.
	var props rawProps
	if json.Unmarshal(raw.Properties, &props) != nil || props.SessionID == "" {
		var env struct {
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(raw.Properties, &env) == nil && len(env.Data) > 0 {
			_ = json.Unmarshal(env.Data, &props)
		}
	}
	ev.SessionID, ev.RequestID, ev.Reply = props.SessionID, props.RequestID, props.Reply

	switch {
	case strings.HasPrefix(raw.Type, "permission."):
		perm := &Permission{
			ID:        or(props.ID, props.RequestID),
			SessionID: props.SessionID,
		}
		perm.Permission = props.Permission
		if perm.Permission == "" {
			perm.Permission = props.Action // v2: action, e.g. "bash"
		}
		perm.Patterns = props.Patterns
		if len(perm.Patterns) == 0 {
			perm.Patterns = props.Resources
		}
		perm.Always = props.Always
		ev.Permission = perm
		if raw.Type == "permission.replied" || raw.Type == "permission.v2.replied" {
			return true
		}

	case raw.Type == "message.updated":
		var info rawMessageInfo
		if json.Unmarshal(props.Info, &info) == nil {
			ev.Role, ev.MessageID = info.Role, info.ID
		}

	case raw.Type == "message.part.updated", raw.Type == "message.part.delta":
		var part rawPart
		if json.Unmarshal(props.Part, &part) == nil {
			ev.PartID, ev.PartType, ev.Text = part.ID, part.Type, part.Text
			ev.MessageID = part.MessageID
			ev.Tool = part.Tool
			ev.Title = part.Title
			// A finished tool call carries state:{status, title, input}.
			if part.State != nil {
				ev.Status, ev.Title = part.State.Status, part.State.Title
				if ev.Title == "" {
					ev.Title = toolArg(part.State.Input, "command")
				}
			}
			if ev.Status == "" {
				ev.Status = part.Status
			}
		}
	case raw.Type == "session.error":
		var re rawError
		if json.Unmarshal(props.Error, &re) == nil {
			ev.ErrorMessage = re.Data.Message
			if ev.ErrorMessage == "" {
				ev.ErrorMessage = re.Name
			}
		}

	case strings.HasPrefix(raw.Type, "question."):
		// Questions exist on the wire; the pane does not answer them in
		// this build.
		return true
	}
	return true
}

// toolArg digs one string out of a tool's raw input JSON, "" when it is
// absent or not a string.
func toolArg(input json.RawMessage, key string) string {
	var v map[string]any
	if json.Unmarshal(input, &v) != nil {
		return ""
	}
	switch x := v[key].(type) {
	case string:
		return x
	case []any:
		var parts []string
		for _, it := range x {
			if s, ok := it.(string); ok {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, " ")
	}
	return ""
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func escape(s string) string {
	r := strings.NewReplacer("%", "%25", " ", "%20", "/", "%2F", "?", "%3F", "&", "%26", "#", "%23")
	return r.Replace(s)
}

// Message is a message of a session, with its parts inline: what the
// agent said, did and finished.
type Message struct {
	Info MessageInfo `json:"info"`
	Parts []Part    `json:"parts"`
}

// MessageInfo is who spoke and when.
type MessageInfo struct {
	ID        string          `json:"id"`
	Role      string          `json:"role"`
	SessionID string          `json:"sessionID"`
	ParentID  string          `json:"parentID"`
	ProviderID string         `json:"providerID"`
	ModelID   string          `json:"modelID"`
	Time      *struct {
		Created   int64 `json:"created"`
		Completed int64 `json:"completed"`
	} `json:"time"`
	Error *struct {
		Name string `json:"name"`
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
	} `json:"error"`
}

// Part is one thing of a message: text, reasoning, a tool call, a step.
type Part struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	MessageID string `json:"messageID"`
	Text      string `json:"text"`
	Tool      string `json:"tool"`
	CallID    string `json:"callID"`
	State     *struct {
		Status string          `json:"status"`
		Title  string          `json:"title"`
		Input  json.RawMessage `json:"input"`
	} `json:"state"`
}

// Messages lists the messages of a session, oldest first: what the user
// asked, what the agent said and did, and whether the turn is over
// (Info.Time.Completed set on the assistant message).
func (c *Client) Messages(ctx context.Context, sessionID string) ([]Message, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.base+"/session/"+sessionID+"/message", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if !ok2xx(resp.StatusCode) {
		return nil, fmt.Errorf("opencode server answered %d", resp.StatusCode)
	}
	var out []Message
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// PendingPermissions lists the permission requests of every session on
// this server, waiting on the user's yes or no.
func (c *Client) PendingPermissions(ctx context.Context) ([]Permission, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.base+"/permission", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if !ok2xx(resp.StatusCode) {
		return nil, fmt.Errorf("opencode server answered %d", resp.StatusCode)
	}
	var raw []Permission
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	return raw, nil
}
