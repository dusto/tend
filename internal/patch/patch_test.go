package patch

import (
	"errors"
	"testing"

	"github.com/dusto/tend/api"
)

func edit(l1, c1, l2, c2 int, text string) api.TextEdit {
	return api.TextEdit{
		Range:   api.Range{Start: api.Position{Line: l1, ByteCol: c1}, End: api.Position{Line: l2, ByteCol: c2}},
		NewText: text,
	}
}

func mustApply(t *testing.T, base string, edits ...api.TextEdit) string {
	t.Helper()
	out, err := Apply([]byte(base), edits)
	if err != nil {
		t.Fatalf("Apply(%q): %v", base, err)
	}
	return string(out)
}

func TestApplyReplaceInsertDelete(t *testing.T) {
	if got := mustApply(t, "hello", edit(0, 0, 0, 1, "H")); got != "Hello" {
		t.Errorf("replace = %q", got)
	}
	if got := mustApply(t, "ac", edit(0, 1, 0, 1, "b")); got != "abc" {
		t.Errorf("insert = %q", got)
	}
	if got := mustApply(t, "abc", edit(0, 1, 0, 2, "")); got != "ac" {
		t.Errorf("delete = %q", got)
	}
}

func TestApplyIsSingleTransformation(t *testing.T) {
	// Both ranges are against the base; an earlier edit must not shift a later
	// edit's offsets. Given out of order, too.
	got := mustApply(t, "abcdef",
		edit(0, 3, 0, 4, "Y"), // replace "d"
		edit(0, 0, 0, 1, "X"), // replace "a"
	)
	if got != "XbcYef" {
		t.Errorf("single-transformation = %q, want XbcYef", got)
	}
}

func TestApplyMultilineAndJoin(t *testing.T) {
	// Insert on the second line.
	if got := mustApply(t, "ab\ncd", edit(1, 1, 1, 1, "X")); got != "ab\ncXd" {
		t.Errorf("multiline insert = %q", got)
	}
	// Join lines by deleting from end of line 0 to start of line 1.
	if got := mustApply(t, "ab\ncd", edit(0, 2, 1, 0, "")); got != "abcd" {
		t.Errorf("LF join = %q", got)
	}
}

func TestApplyMultibyteByteColumns(t *testing.T) {
	// "é" is two UTF-8 bytes, so the 'l' starts at byte_col 3, not 2.
	base := "héllo"
	// Replace "é" (bytes 1..3) with "e".
	if got := mustApply(t, base, edit(0, 1, 0, 3, "e")); got != "hello" {
		t.Errorf("multibyte replace = %q, want hello", got)
	}
	// A column in the middle of the multibyte rune is still a valid byte offset;
	// the engine works in bytes, so inserting at byte_col 2 splits the rune. That
	// is the caller's responsibility — what matters is byte_col is a byte offset.
	if got := mustApply(t, base, edit(0, 3, 0, 3, "X")); got != "héXllo" {
		t.Errorf("insert after rune = %q", got)
	}
}

func TestApplyPreservesCRLF(t *testing.T) {
	// Editing line content never touches the CRLF terminator.
	if got := mustApply(t, "a\r\nb", edit(0, 0, 0, 1, "X")); got != "X\r\nb" {
		t.Errorf("CRLF preserved = %q", got)
	}
	// Joining a CRLF line removes the whole \r\n.
	if got := mustApply(t, "ab\r\ncd", edit(0, 2, 1, 0, "")); got != "abcd" {
		t.Errorf("CRLF join = %q", got)
	}
}

func TestApplyRejectsColumnBetweenCRandLF(t *testing.T) {
	// Line 0 is "a\r"; byte_col 2 points at the '\n', between '\r' and '\n'.
	_, err := Apply([]byte("a\r\nb"), []api.TextEdit{edit(0, 2, 0, 2, "x")})
	if !errors.Is(err, ErrInvalidPosition) {
		t.Errorf("err = %v, want ErrInvalidPosition", err)
	}
}

func TestApplyRejectsOutOfRange(t *testing.T) {
	cases := []api.TextEdit{
		edit(5, 0, 5, 0, "x"),  // no such line
		edit(0, 9, 0, 9, "x"),  // column past end of line
		edit(0, -1, 0, 0, "x"), // negative column
	}
	for i, e := range cases {
		if _, err := Apply([]byte("abc"), []api.TextEdit{e}); !errors.Is(err, ErrInvalidPosition) {
			t.Errorf("case %d: err = %v, want ErrInvalidPosition", i, err)
		}
	}
}

func TestApplyRejectsInvalidAndOverlapping(t *testing.T) {
	if _, err := Apply([]byte("abc"), []api.TextEdit{edit(0, 2, 0, 1, "x")}); !errors.Is(err, ErrInvalidRange) {
		t.Errorf("start>end err = %v, want ErrInvalidRange", err)
	}
	overlap := []api.TextEdit{edit(0, 0, 0, 2, "X"), edit(0, 1, 0, 3, "Y")}
	if _, err := Apply([]byte("abcd"), overlap); !errors.Is(err, ErrOverlap) {
		t.Errorf("overlap err = %v, want ErrOverlap", err)
	}
}

func TestApplyAdjacentEditsAllowed(t *testing.T) {
	// Touching (non-overlapping) ranges compose; two inserts at the same offset
	// keep input order.
	got := mustApply(t, "ad",
		edit(0, 1, 0, 1, "b"),
		edit(0, 1, 0, 1, "c"),
	)
	if got != "abcd" {
		t.Errorf("adjacent inserts = %q, want abcd", got)
	}
}

func TestApplyInsertAtReplacementStartIsOrderIndependent(t *testing.T) {
	// A zero-length insert at the start of a replacement is touching, not
	// overlapping, and must apply before the replacement regardless of input
	// order: "abcd" with [1,3)->"X" and [1,1)->"Y" -> "aYXd".
	want := "aYXd"
	if got := mustApply(t, "abcd", edit(0, 1, 0, 3, "X"), edit(0, 1, 0, 1, "Y")); got != want {
		t.Errorf("replacement-first = %q, want %q", got, want)
	}
	if got := mustApply(t, "abcd", edit(0, 1, 0, 1, "Y"), edit(0, 1, 0, 3, "X")); got != want {
		t.Errorf("insert-first = %q, want %q", got, want)
	}
}

func TestApplyEmptyBase(t *testing.T) {
	if got := mustApply(t, "", edit(0, 0, 0, 0, "hi")); got != "hi" {
		t.Errorf("insert into empty = %q", got)
	}
}
