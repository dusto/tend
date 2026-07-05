package agent

import (
	"unicode/utf8"

	"github.com/dusto/tend/api"
)

// charsPerToken is the rule-of-thumb ratio the approximate token estimate uses.
// It is model-agnostic: the daemon does not run a per-model tokenizer, so the
// estimate is deliberately coarse and always flagged Approximate.
const charsPerToken = 4

// measurePromptUsage measures the prompt input a turn sends. Only text blocks
// contribute to the byte/char/token counts; resource links and image/audio blobs
// are counted as attachments, since their content is not sent as prompt text
// (the agent resolves links itself, and blobs are not text the estimate can
// tokenize). It mirrors promptBlocks: an empty Content sends a single text block
// from Text.
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

// approxTokens estimates tokens from a character count, rounding up so any
// non-empty text reports at least one token.
func approxTokens(chars int) int {
	if chars == 0 {
		return 0
	}
	return (chars + charsPerToken - 1) / charsPerToken
}
