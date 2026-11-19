package main

import (
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	"mayoi/internal/workspace"
)

// mentionComplete is the @-completion dropdown while the user types a
// file reference in the composer.
type mentionComplete struct {
	at    int
	query string
	sel   int
	cands []string
}

// updateMention reads the composer's caret and, if an "@<query>" sits
// there, lists the files that match. The handle holds the editor state
// of the last build, so caret is one frame stale at typing speed.
func (a *app) updateMention(c *ui.Context) {
	st := &a.chat
	if a.index == nil || len(a.index) == 0 {
		// The chat pane never opens Quick Open or search itself; ask for the
		// folder snapshot so @-completion works from the cold state.
		a.refreshIndex()
		st.mention = nil
		return
	}
	area := c.Resolve(st.input)
	if !area.Valid() {
		return
	}
	caret, _ := area.TextSelection()
	if caret <= 0 {
		st.mention = nil
		return
	}
	byteCaret := runeByte(st.composer, caret)
	if byteCaret > len(st.composer) {
		byteCaret = len(st.composer)
	}
	at := strings.LastIndexByte(st.composer[:byteCaret], '@')
	if at < 0 {
		st.mention = nil
		return
	}
	query := st.composer[at+1 : byteCaret]
	if len(query) > 64 {
		st.mention = nil
		return
	}
	for _, r := range query {
		if !isFileChar(r) {
			st.mention = nil
			return
		}
	}
	if query == "" {
		st.mention = nil
		return
	}
	cands := workspace.Fuzzy(a.index, query, 8)
	paths := make([]string, 0, len(cands))
	for _, match := range cands {
		if match.Index < len(a.index) {
			paths = append(paths, a.index[match.Index])
		}
	}
	if len(paths) == 0 {
		st.mention = nil
		return
	}
	if st.mention != nil && st.mention.at == at && st.mention.query == query {
		st.mention.cands = paths
		if st.mention.sel >= len(paths) {
			st.mention.sel = len(paths) - 1
		}
		return
	}
	st.mention = &mentionComplete{at: at, query: query, sel: 0, cands: paths}
}

func isFileChar(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '_' || r == '.' || r == '/' || r == '-' || r == '+':
		return true
	}
	return false
}

func (a *app) chatMentionChoose(c *ui.Context, m *mentionComplete, chosen string) {
	st := &a.chat
	end := m.at + 1 + len(m.query)
	st.composer = st.composer[:m.at] + "@" + chosen + st.composer[end:]
	st.mention = nil
	// The element's caret is in runes; count runes up to the end of the
	// inserted text.
	runes := 0
	for i := 0; i < m.at+1+len(chosen) && i < len(st.composer); {
		_, size := utf8.DecodeRuneInString(st.composer[i:])
		i += size
		runes++
	}
	if area := a.chatArea(c); area.Valid() {
		area.SetTextSelection(runes, runes)
	}
	st.input.Focus()
	a.invalidate()
}

// chatArea resolves the composer to its Element from the last build.
func (a *app) chatArea(c *ui.Context) ui.Element {
	return c.Resolve(a.chat.input)
}
