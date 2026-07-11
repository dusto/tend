package agent

import (
	"strings"
	"unicode/utf8"

	"github.com/dusto/tend/api"
)

// charsPerToken is the rule-of-thumb ratio the approximate token estimate uses.
// It is model-agnostic: the daemon does not run a per-model tokenizer, so the
// estimate is deliberately coarse and always flagged Approximate.
const charsPerToken = 4

// measurePromptUsage measures the prompt input composed for a turn. Only text
// blocks contribute to the byte/char/token counts; resource links and image/audio
// blobs are counted as attachments, since their content is not composed as prompt
// text (the agent resolves links itself, and blobs are not text the estimate can
// tokenize). It mirrors promptBlocks: an empty Content composes a single text
// block from Text.
func measurePromptUsage(id api.SessionID, p api.AgentPromptParams) api.AgentPromptUsage {
	u := api.AgentPromptUsage{SessionID: id, Approximate: true}
	if len(p.Content) == 0 {
		u.Blocks = 1
		u.TextBytes = len(p.Text)
		u.TextChars = utf8.RuneCountInString(p.Text)
		u.TokensApprox = approxTokens(u.TextChars)
		return u
	}
	u.Blocks = len(p.Content)
	for _, c := range p.Content {
		if c.Type == api.PromptContentText {
			u.TextBytes += len(c.Text)
			u.TextChars += utf8.RuneCountInString(c.Text)
			continue
		}
		u.Attachments++
	}
	u.TokensApprox = approxTokens(u.TextChars)
	return u
}

// userPrompt composes the user's prompt content for a turn into a UserPrompt
// event payload: the text (the plain Text, or the concatenated text blocks, to
// mirror promptBlocks) plus a count of non-text attachment blocks, whose content
// is not persisted. It parallels measurePromptUsage's block handling.
func userPrompt(id api.SessionID, p api.AgentPromptParams) api.UserPrompt {
	if len(p.Content) == 0 {
		return api.UserPrompt{SessionID: id, Text: p.Text}
	}
	up := api.UserPrompt{SessionID: id}
	var text strings.Builder
	for _, c := range p.Content {
		if c.Type == api.PromptContentText {
			text.WriteString(c.Text)
			continue
		}
		up.Attachments++
	}
	up.Text = text.String()
	return up
}

// approxTokens estimates tokens from a character count, rounding up so any
// non-empty text reports at least one token.
func approxTokens(chars int) int {
	if chars == 0 {
		return 0
	}
	return (chars + charsPerToken - 1) / charsPerToken
}
