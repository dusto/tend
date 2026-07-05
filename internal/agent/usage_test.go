package agent

import (
	"testing"

	"github.com/dusto/tend/api"
)

func TestMeasurePromptUsage(t *testing.T) {
	tests := []struct {
		name string
		p    api.AgentPromptParams
		want api.AgentPromptUsage
	}{
		{
			name: "plain text sends one block",
			p:    api.AgentPromptParams{Text: "hello"},
			want: api.AgentPromptUsage{TextBytes: 5, TextChars: 5, TokensApprox: 2, Blocks: 1, Approximate: true},
		},
		{
			name: "empty text is one block, zero tokens",
			p:    api.AgentPromptParams{Text: ""},
			want: api.AgentPromptUsage{Blocks: 1, Approximate: true},
		},
		{
			name: "multibyte text: bytes exceed chars",
			p:    api.AgentPromptParams{Text: "héllo"}, // 'é' is 2 bytes
			want: api.AgentPromptUsage{TextBytes: 6, TextChars: 5, TokensApprox: 2, Blocks: 1, Approximate: true},
		},
		{
			name: "content blocks: only text counts, others are attachments",
			p: api.AgentPromptParams{Content: []api.PromptContentBlock{
				{Type: api.PromptContentText, Text: "abcd"},
				{Type: api.PromptContentResourceLink, URI: "file:///x"},
				{Type: api.PromptContentImage, MimeType: "image/png", Data: "AAAA"},
			}},
			want: api.AgentPromptUsage{TextBytes: 4, TextChars: 4, TokensApprox: 1, Blocks: 3, Attachments: 2, Approximate: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := measurePromptUsage("", tt.p)
			if got != tt.want {
				t.Errorf("measurePromptUsage() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestMeasurePromptUsageCarriesSessionID(t *testing.T) {
	if got := measurePromptUsage("sess-9", api.AgentPromptParams{Text: "x"}); got.SessionID != "sess-9" {
		t.Errorf("SessionID = %q, want sess-9", got.SessionID)
	}
}
