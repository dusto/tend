package memimport

import (
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// mdFile is a parsed markdown source file: its YAML frontmatter (raw, for the
// adapter to decode into its own shape) and the body after the frontmatter.
type mdFile struct {
	frontmatter []byte
	body        string
}

// readMarkdown reads path and splits off a leading YAML frontmatter block fenced
// by --- lines. A file with no frontmatter yields the whole content as body.
func readMarkdown(path string) (mdFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return mdFile{}, err
	}
	fm, body := splitFrontmatter(data)
	return mdFile{frontmatter: fm, body: strings.TrimSpace(string(body))}, nil
}

// decodeFrontmatter unmarshals the file's frontmatter into v (a no-op when there
// is none), so an adapter reads only the keys it cares about.
func (f mdFile) decodeFrontmatter(v any) error {
	if len(f.frontmatter) == 0 {
		return nil
	}
	return yaml.Unmarshal(f.frontmatter, v)
}

// splitFrontmatter separates a leading YAML frontmatter block (fenced by --- on
// its own line) from the body. With no frontmatter it returns nil and the whole
// input as body.
func splitFrontmatter(data []byte) (fm, body []byte) {
	s := string(data)
	var rest string
	switch {
	case strings.HasPrefix(s, "---\n"):
		rest = s[len("---\n"):]
	case strings.HasPrefix(s, "---\r\n"):
		rest = s[len("---\r\n"):]
	default:
		return nil, data
	}
	for _, fence := range []string{"\n---\n", "\n---\r\n"} {
		if before, after, ok := strings.Cut(rest, fence); ok {
			return []byte(before), []byte(after)
		}
	}
	if before, ok := strings.CutSuffix(rest, "\n---"); ok {
		return []byte(before), nil
	}
	return nil, data
}

// firstHeading returns the text of the first markdown ATX heading (#...) in body,
// or "" when there is none.
func firstHeading(body string) string {
	for line := range strings.SplitSeq(body, "\n") {
		t := strings.TrimSpace(line)
		if h, ok := strings.CutPrefix(t, "#"); ok {
			return strings.TrimSpace(strings.TrimLeft(h, "#"))
		}
	}
	return ""
}

// titleOr returns the body's first heading, falling back to fallback.
func titleOr(body, fallback string) string {
	if h := firstHeading(body); h != "" {
		return h
	}
	return fallback
}

// slug lowercases s and collapses non-alphanumeric runs to single hyphens, so a
// filename becomes a stable id segment.
func slug(s string) string {
	var b strings.Builder
	prevHyphen := false
	for _, r := range strings.ToLower(s) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevHyphen = false
		default:
			if !prevHyphen && b.Len() > 0 {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// flexStrings decodes a YAML value that may be a single scalar or a sequence of
// scalars into a string slice (Kiro's fileMatchPattern, Cursor's globs).
type flexStrings []string

func (f *flexStrings) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.SequenceNode {
		var xs []string
		if err := node.Decode(&xs); err != nil {
			return err
		}
		*f = xs
		return nil
	}
	var s string
	if err := node.Decode(&s); err != nil {
		return err
	}
	if s = strings.TrimSpace(s); s != "" {
		*f = []string{s}
	}
	return nil
}
