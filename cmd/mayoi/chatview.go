package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"mayoi/internal/opencode"
	"mayoi/locales"
)

// chatRow is one bubble of the transcript: the user's prompt or the
// agent's turn.
type chatRow struct {
	role   string // "user" | "assistant"
	parts  []chatPart
	tools  []chatTool
	status string // "streaming" | "done" | "error"
	err    string
}

type chatPart struct {
	id   string
	typ  string // "text" | "reasoning"
	text string
}

type chatTool struct {
	id     string
	name   string
	title  string
	status string // "running" | "done" | "error"
}

func (r *chatRow) text() string {
	parts := make([]string, 0, len(r.parts))
	for _, p := range r.parts {
		if p.typ == "text" && p.text != "" {
			parts = append(parts, p.text)
		}
	}
	return strings.Join(parts, "\n")
}

// chatAsk is the agent's pending permission request, with how the user
// answered it once they had.
type chatAsk struct {
	perm    opencode.Permission
	decided string // "" | "once" | "always" | "reject"
}

type chatState struct {
	sessionID     string
	model         opencode.Model
	hasModel      bool
	models        []opencode.Model
	modelsLoading bool
	modelsErr     string
	modelsAttempt time.Time
	showMenu      bool

	rows     []chatRow
	ask      []chatAsk
	busy     bool
	composer string
	input    ui.Handle
	scroll   ui.ScrollState

	// cancel stops the events pump of the turn running, if any.
	cancel context.CancelFunc
	busySince time.Time
}

func (st *chatState) reset(a *app) {
	st.sessionID, st.rows, st.ask = "", nil, nil
	st.busy = false
	st.busySince = time.Time{}
	if st.cancel != nil {
		st.cancel()
		st.cancel = nil
	}
	a.invalidate()
}

// send posts the composer as a prompt to the agent: a session is made
// for the workspace the first time, the server runs the turn itself, and
// the pane follows it on the event bus.
func (st *chatState) send(a *app) {
	text := strings.TrimSpace(st.composer)
	if text == "" || st.busy {
		return
	}
	if !st.hasModel {
		st.rows = append(st.rows, chatRow{role: "assistant", status: "error", err: locales.ChatPickModel()})
		a.invalidate()
		return
	}
	dir := a.folder()
	st.busy = true
	st.busySince = time.Now()
	st.composer = ""
	st.rows = append(st.rows, chatRow{role: "user", parts: []chatPart{{id: "u" + time.Now().Format("150405"), typ: "text", text: text}}})
	providerID, modelID := st.model.ProviderID, st.model.ID
	sid := st.sessionID
	ctx, cancel := context.WithCancel(context.Background())
	st.cancel = cancel
	a.invalidate()

	go func() {
		cl := opencode.New()
		if sid == "" {
			newSID, err := cl.NewSession(ctx, dir)
			if err != nil {
				a.post(func() { st.fail(a, err) })
				return
			}
			sid = newSID
			a.post(func() { st.sessionID = newSID })
		}
		if err := cl.Prompt(ctx, sid, text, providerID, modelID); err != nil {
			a.post(func() { st.fail(a, err) })
			return
		}
		// The server's event stream carries only heartbeats for clients
		// outside the frontmost TUI, so the pane follows the turn by
		// asking for the message list, every half second.
		turn := &turnState{rows: map[string]int{}}
		turnFailures := 0
		for {
			select {
			case <-ctx.Done():
				a.post(func() { st.finishTurn(a) })
				return
			case <-time.After(400 * time.Millisecond):
			}
			ms, err := cl.Messages(ctx, sid)
			if err != nil {
				turnFailures++
				if turnFailures > 10 {
					a.post(func() { st.fail(a, fmt.Errorf("checking the reply failed: %w", err)) })
					return
				}
				continue
			}
			turnFailures = 0
			done := true
			for i := range ms {
				if ms[i].Info.Role == "assistant" && (ms[i].Info.Time == nil || ms[i].Info.Time.Completed == 0) {
					done = false
					break
				}
				if ms[i].Info.Error != nil {
					done = true
				}
			}
			pending, err := cl.PendingPermissions(ctx)
			if err != nil {
				pending = nil
			}
			pending = filterPending(pending, sid)
			m := ms
			p := pending
			a.post(func() { a.applyTurnMessages(a, turn, sid, m, p) })
			if len(pending) > 0 {
				continue
			}
			if done {
				// One last read to land the final text, then finish.
				a.post(func() { st.finishTurn(a) })
				return
			}
		}
	}()
}

// turnState tracks the client-side progress of a single prompt's reply.
type turnState struct {
	// rows maps opencode message ids to the transcript rows streaming
	// into.
	rows map[string]int
}

// filterPending keeps the permissions of this session.
func filterPending(pps []opencode.Permission, id string) []opencode.Permission {
	out := pps[:0]
	for _, p := range pps {
		if p.SessionID == id {
			out = append(out, p)
		}
	}
	return out
}

// applyTurnMessages folds the messages read so far into the transcript:
// the assistant's turn, its tool calls, its completion, and the approvals
// waiting on the user, each render replacing the previous one.
func (a *app) applyTurnMessages(_ *app, turn *turnState, sid string, ms []opencode.Message, pending []opencode.Permission) {
	st := &a.chat
	if sid == "" || sid != st.sessionID {
		return
	}
	for _, m := range ms {
		if m.Info.Role != "assistant" {
			continue
		}
		idx, ok := turn.rows[m.Info.ID]
		if !ok {
			idx = len(st.rows)
			turn.rows[m.Info.ID] = idx
			st.rows = append(st.rows, chatRow{role: "assistant", status: "streaming"})
		}
		r := &st.rows[idx]
		var text string
		var tools []chatTool
		for _, p := range m.Parts {
			switch p.Type {
			case "text":
				if p.Text != "" {
					text = p.Text
				}
			case "tool":
				name := p.Tool
				if name == "" {
					name = "tool"
				}
				t := chatTool{id: p.ID, name: name, status: "running"}
				if p.State != nil {
					t.title = clip(p.State.Title, 160)
					switch p.State.Status {
					case "completed", "done", "success":
						t.status = "done"
					case "error":
						t.status = "error"
					}
					if t.title == "" {
						t.title = clip(toolArg(p.State.Input, "command"), 160)
					}
				}
				tools = append(tools, t)
			}
		}
		if text != "" {
			updated := false
			for i, p := range r.parts {
				if p.id == "m"+m.Info.ID {
					r.parts[i].text = text
					updated = true
					break
				}
			}
			if !updated {
				r.parts = append(r.parts, chatPart{id: "m" + m.Info.ID, typ: "text", text: text})
			}
		}
		r.tools = tools
		if m.Info.Error != nil {
			msg := m.Info.Error.Data.Message
			if msg == "" {
				msg = m.Info.Error.Name
			}
			if msg != "" {
				r.err = msg
				r.status = "error"
			}
		} else if m.Info.Time != nil && m.Info.Time.Completed != 0 {
			r.status = "done"
		} else {
			r.status = "streaming"
		}
	}
	for _, p := range pending {
		if p.SessionID != sid {
			continue
		}
		known := false
		for _, q := range st.ask {
			if q.perm.ID == p.ID {
				known = true
				break
			}
		}
		if !known {
			st.ask = append(st.ask, chatAsk{perm: p})
		}
	}
	if st.scroll.Y >= st.scroll.MaxY-1 {
		st.scroll.Y = math.MaxFloat32
	}
	a.invalidate()
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

// fail starts no new turn: the composer ran into a wall.
func (st *chatState) fail(a *app, err error) {
	st.busy = false
	st.busySince = time.Time{}
	if st.cancel != nil {
		st.cancel()
		st.cancel = nil
	}
	st.rows = append(st.rows, chatRow{role: "assistant", status: "error", err: err.Error()})
	a.invalidate()
}

// finishTurn ends the running turn: the working rows are done and the
// events pump stops.
func (st *chatState) finishTurn(a *app) {
	for i := range st.rows {
		if st.rows[i].role == "assistant" && st.rows[i].status == "streaming" {
			st.rows[i].status = "done"
		}
	}
	st.busy = false
	st.busySince = time.Time{}
	if st.cancel != nil {
		st.cancel()
		st.cancel = nil
	}
	a.invalidate()
}

// chatReply answers the agent's permission request: once, always or
// rejected. It writes the decision to the server and marks the card.
func (a *app) chatReply(ask *chatAsk, reply string) {
	id := ask.perm.ID
	ask.decided = reply // optimistic; the server's replied event confirms it
	a.invalidate()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = opencode.New().ReplyPermission(ctx, id, reply)
	}()
}

// loadChatModelsOnce starts the one fetch of the models of the connected
// providers; after it ends it waits four seconds before trying again if
// the server never answered.
func (a *app) loadChatModelsOnce() {
	st := &a.chat
	if st.models != nil || st.modelsLoading {
		return
	}
	if !st.modelsAttempt.IsZero() && time.Since(st.modelsAttempt) < 4*time.Second {
		return
	}
	st.modelsLoading, st.modelsErr, st.modelsAttempt = true, "", time.Now()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ms, err := opencode.New().Models(ctx)
		a.post(func() {
			st.modelsLoading = false
			if err != nil {
				st.modelsErr = err.Error()
			} else {
				st.models, st.modelsErr = ms, ""
				if !st.hasModel {
					for _, m := range ms {
						if m.Free {
							st.model, st.hasModel = m, true
							break
						}
					}
					if !st.hasModel && len(ms) > 0 {
						st.model, st.hasModel = ms[0], true
					}
				}
			}
			a.invalidate()
		})
	}()
}

func clip(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// chatSpinner shows something is happening while a turn runs: a glyph
// walking through its steps and the seconds the turn has taken.
func (a *app) chatSpinner(c *ui.Context) {
	p := a.pal
	st := &a.chat
	in := time.Since(st.busySince)
	if in < 0 {
		in = 0
	}
	glyph := spinGlyphs[int(in/spinStep)%len(spinGlyphs)]
	secs := int(in.Seconds())
	ui.Row(c).Gap(4).AlignItems(ui.Center).
		Tooltip(fmt.Sprintf("OpenCode is working (%ds)", secs)).Children(func() {
		ui.Text(c, glyph).FontSize(11.5).TextColor(p.accent)
		ui.Text(c, fmt.Sprintf("Working… %ds", secs)).FontSize(10.5).TextColor(p.muted).SingleLine()
	})
	c.AnimationFrame()
}

// showChat shows the OpenCode chat in the sidebar, like the Sessions view.
func (a *app) showChat() {
	if a.folder() == "" {
		return
	}
	a.window.SidebarClosed, a.window.SidebarView = false, "chat"
	a.settings.Changed()
	a.chat.input.Focus()
}

// chatView draws the pane: the header, the transcript, the approvals, the
// composer.
func (a *app) chatView(c *ui.Context) {
	p, st := a.pal, &a.chat
	go a.loadChatModelsOnce()

	ui.Column(c).Fill().Background(p.chrome).Children(func() {
		ui.Row(c).Height(30).Padding(0, 6, 0, 10).Gap(4).AlignItems(ui.Center).Children(func() {
			ui.Text(c, locales.SidebarChat()).FontSize(11).FontWeight(600).LetterSpacing(0.4).
				TextColor(p.muted).Grow(1).SingleLine().Padding(0, 2)
			if st.modelsLoading {
				ui.Text(c, locales.ChatModelsLoading()).FontSize(10.5).TextColor(p.muted).SingleLine()
			} else if st.modelsErr != "" {
				ui.Text(c, locales.ChatModelsError()).FontSize(10.5).TextColor(p.dangerFg).SingleLine()
			} else {
				name := st.model.Name
				if name == "" {
					name = locales.ChatPickModel()
				}
				btn := ui.ButtonBase(c.Key("chat-model")).Height(22).Padding(0, 6).Gap(3).Radius(6).
					Justify(ui.Center).AlignItems(ui.Center).Label(locales.ChatModel()).
					Children(func() {
						ui.Text(c, clip(name, 26)).FontSize(10.5).SingleLine().TextColor(p.text).MaxWidth(150)
						ui.Icon(c, iconChevronDown).FontSize(10).TextColor(p.muted).Padding(0, 2, 0, 0)
					})
				if btn.Clicked() {
					st.showMenu = !st.showMenu
				}
			}
			if st.busy {
				a.chatSpinner(c)
			}
			if b := a.iconButton(c, iconPlus, locales.ChatNewChat(), false); b.Clicked() {
				st.reset(a)
			}
		})

		// The model menu, right under the header, until the user picks.
		if st.showMenu {
			a.chatModelMenu(c)
		}

		// The writer: the transcript.
		scroll := ui.Scroll(c.Key("chat-transcript")).Grow(1).MinHeight(0).
			Padding(4, 10, 8, 10).TrackScroll(&st.scroll)
		scroll.Children(func() {
			ui.Column(c).Gap(8).Children(func() {
				if len(st.rows) == 0 {
					a.chatEmpty(c)
					return
				}
				for i := range st.rows {
					a.chatBubble(c, &st.rows[i])
				}
				for i := range st.ask {
					a.chatApproval(c, &st.ask[i])
				}
			})
		})

		// The composer.
		ui.Box(c).Shrink(0).BorderWidth(1, 0, 0, 0).BorderColor(p.border).
			Padding(6, 8, 10, 8).Background(p.chrome).Children(func() {
			field := ui.Row(c).MinHeight(54).Radius(8).
				Background(p.editor).Border(1, p.border).Padding(4, 6).AlignItems(ui.Center).Gap(4)
			field.Children(func() {
				area := a.textKeys(ui.TextAreaBase(c.Key("chat-prompt"), &st.composer)).
					Grow(1).Lines(2, 8).MinWidth(0).FontSize(12.5).Padding(2).
					Placeholder(locales.ChatPlaceholder()).Label(locales.ChatPlaceholder())
				area.Bind(&st.input)
				if area.Shortcut(ui.Ctrl, ui.KeyEnter) || area.Shortcut(ui.Cmd, ui.KeyEnter) {
					st.send(a)
					return
				}
				if st.busy {
					stop := ui.ButtonBase(c.Key("chat-stop")).Size(26, 26).Radius(8).
						Justify(ui.Center).AlignItems(ui.Center).Label(locales.ChatStop()).Tooltip(locales.ChatStop())
					stop.Children(func() {
						ui.Icon(c, ui.MustParseSVG(lucideSVG(`<rect x="9" y="9" width="6" height="6" rx="1"/>`))).FontSize(13).TextColor(p.muted)
					})
					if stop.Clicked() && st.cancel != nil {
						st.cancel()
						st.busy = false
						st.finishTurn(a)
					}
				} else {
					send := ui.ButtonBase(c.Key("chat-send")).Size(26, 26).Radius(8).
						Justify(ui.Center).AlignItems(ui.Center).Label(locales.ChatSend()).Tooltip(locales.ChatSend())
					send.Children(func() { ui.Icon(c, iconSend).FontSize(15).TextColor(p.accent) })
					if send.Clicked() {
						st.send(a)
					}
				}
			})
		})
	})
}

// chatEmpty says what the pane is for.
func (a *app) chatEmpty(c *ui.Context) {
	p := a.pal
	ui.Column(c).AlignItems(ui.Center).Padding(28, 14).Gap(8).Children(func() {
		box := ui.Box(c).Size(44, 44).Radius(22).Background(p.accentDim.Alpha(0.4)).
			AlignItems(ui.Center).Justify(ui.Center)
		box.Children(func() {
			ui.Icon(c, iconAgent).FontSize(22).TextColor(p.accent)
		})
		ui.Text(c, locales.ChatEmptyTitle()).FontSize(11.5).FontWeight(600).TextColor(p.text).TextAlign(ui.Center)
		ui.Text(c, locales.ChatEmptyBody()).FontSize(11).LineHeight(1.5).TextColor(p.muted).TextAlign(ui.Center)
	})
}

// chatBubble draws one turn.
func (a *app) chatBubble(c *ui.Context, r *chatRow) {
	p := a.pal
	name, nameColor := "", p.accent
	if r.role == "assistant" {
		name, nameColor = locales.ChatFromAgent(), p.muted
	} else {
		name = locales.ChatFromUser()
	}
	ui.Box(c).Radius(10).Background(p.editor).Padding(7, 10, 7, 10).Children(func() {
		ui.Column(c).Gap(4).Children(func() {
			ui.Text(c, name).FontSize(9).FontWeight(700).LetterSpacing(0.7).TextColor(nameColor).SingleLine()
			if r.role == "assistant" && r.status == "streaming" && r.text() == "" && len(r.tools) == 0 {
				in := time.Since(a.chat.busySince)
				if in < 0 {
					in = 0
				}
				glyph := spinGlyphs[int(in/spinStep)%len(spinGlyphs)]
				ui.Text(c, glyph+"  "+locales.ChatThinking()).FontSize(11.5).TextColor(p.muted)
				c.AnimationFrame()
			} else if r.text() != "" {
				ui.Text(c, r.text()).FontSize(12).LineHeight(1.5).TextColor(p.text).Selectable()
			}
			for _, t := range r.tools {
				ui.Row(c).Gap(6).AlignItems(ui.Center).MinHeight(18).Children(func() {
					mark, markCol := "▪", p.accent
					switch t.status {
					case "done":
						mark, markCol = "✓", p.added
					case "error":
						mark, markCol = "✗", p.dangerFg
					}
					ui.Text(c, mark).FontSize(10.5).TextColor(markCol)
					line := t.name
					if t.title != "" {
						line = line + "  " + t.title
					}
					ui.Text(c, clip(line, 200)).FontSize(10.5).TextColor(p.muted).SingleLine().Grow(1)
				})
			}
			if r.err != "" {
				ui.Text(c, r.err).FontSize(11.5).TextColor(p.dangerFg).Selectable()
			}
		})
	})
}

// chatApproval draws one pending approval of the agent, with the way to
// answer it: once, always, or no.
func (a *app) chatApproval(c *ui.Context, ask *chatAsk) {
	p := a.pal
	ui.Box(c).Radius(10).Border(1, p.accentDim).Padding(7, 10, 7, 10).Background(p.editor).Children(func() {
		ui.Column(c).Gap(5).Children(func() {
			ui.Text(c, locales.ChatApprovalTitle()).FontSize(9).FontWeight(700).LetterSpacing(0.7).TextColor(p.muted).SingleLine()
			what := ask.perm.Permission
			if what == "" {
				what = "tool"
			}
			ui.Text(c, what).FontSize(11.5).FontWeight(600).TextColor(p.text).Selectable()
			if len(ask.perm.Patterns) > 0 {
				ui.Text(c, clip(strings.Join(ask.perm.Patterns, "\n"), 400)).
					FontSize(10.5).TextColor(p.muted).Selectable()
			}
			if ask.decided == "" {
				ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
					for _, kv := range [][2]string{
						{locales.ChatApproveOnce(), "once"},
						{locales.ChatApproveAlways(), "always"},
						{locales.ChatReject(), "reject"},
					} {
						label, reply := kv[0], kv[1]
						b := ui.ButtonBase(c.Key("chat-perm-" + reply)).Height(22).Padding(0, 9).Radius(6).
							Label(label).Border(1, p.border).Justify(ui.Center).AlignItems(ui.Center)
						if reply == "reject" {
							b.Background(p.editor)
						} else {
							b.Background(p.accentDim.Alpha(0.5))
						}
						if b.Clicked() {
							a.chatReply(ask, reply)
						}
					}
				})
			} else {
				note := locales.ChatRejected()
				if ask.decided == "once" {
					note = locales.ChatApprovedOnce()
				} else if ask.decided == "always" {
					note = locales.ChatApprovedAlways()
				}
				ui.Text(c, note).FontSize(10.5).TextColor(p.muted).SingleLine()
			}
		})
	})
}

// chatModelMenu lists the models of the connected providers, the free
// ones first, twenty rows at most.
func (a *app) chatModelMenu(c *ui.Context) {
	p, st := a.pal, &a.chat
	ui.Column(c).BorderWidth(0, 0, 1, 0).BorderColor(p.border).Padding(3, 6, 6, 6).Children(func() {
		scroll := ui.Scroll(c.Key("chat-models")).MaxHeight(252).Children(func() {
			ui.Column(c).Gap(1).Children(func() {
				const limit = 20
				count := 0
				for _, m := range st.models {
					if count >= limit {
						break
					}
					count++
					m := m
					name := m.Name
					if m.Free {
						name += "  · free"
					}
					checked := st.hasModel && st.model.ProviderID == m.ProviderID && st.model.ID == m.ID
					key := "chat-model-" + m.ProviderID + "-" + m.ID
					b := ui.ButtonBase(c.Key(key)).Height(24).Padding(0, 6).Gap(6).Radius(5).
						Justify(ui.Start).AlignItems(ui.Center).Label(name)
					if checked {
						b.Background(p.hover)
					}
					if b.Hovered() {
						b.Background(p.hover)
					}
					b.Children(func() {
						if checked {
							ui.Text(c, "✓").FontSize(11).TextColor(p.accent).Padding(0, 2)
						}
						ui.Text(c, clip(name, 48)).FontSize(11).TextColor(p.text).SingleLine().Grow(1)
					})
					if b.Clicked() {
						st.model, st.hasModel, st.showMenu = m, true, false
						a.invalidate()
					}
				}
			})
		})
		_ = scroll
	})
}
