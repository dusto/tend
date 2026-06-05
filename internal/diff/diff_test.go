package diff

import "testing"

func TestUnifiedEqualIsEmpty(t *testing.T) {
	if got := Unified("a\nb\n", "a\nb\n"); got != "" {
		t.Errorf("equal diff = %q, want empty", got)
	}
}

func TestUnifiedSingleLineChange(t *testing.T) {
	got := Unified("a\nb\nc\n", "a\nB\nc\n")
	want := "@@ -1,3 +1,3 @@\n a\n-b\n+B\n c\n"
	if got != want {
		t.Errorf("diff =\n%q\nwant\n%q", got, want)
	}
}

func TestUnifiedInsertAndDelete(t *testing.T) {
	// Pure insertion at the end.
	got := Unified("a\n", "a\nb\n")
	want := "@@ -1 +1,2 @@\n a\n+b\n"
	if got != want {
		t.Errorf("insert diff =\n%q\nwant\n%q", got, want)
	}
	// Pure deletion.
	got = Unified("a\nb\n", "a\n")
	want = "@@ -1,2 +1 @@\n a\n-b\n"
	if got != want {
		t.Errorf("delete diff =\n%q\nwant\n%q", got, want)
	}
}

func TestUnifiedSeparateHunks(t *testing.T) {
	// Two changes far apart produce two hunks.
	old := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n"
	new := "X\n2\n3\n4\n5\n6\n7\n8\n9\nY\n"
	got := Unified(old, new)
	// First hunk covers the change at line 1 (+context), second covers line 10.
	wantFirst := "@@ -1,4 +1,4 @@\n-1\n+X\n 2\n 3\n 4\n"
	wantSecond := "@@ -7,4 +7,4 @@\n 7\n 8\n 9\n-10\n+Y\n"
	if got != wantFirst+wantSecond {
		t.Errorf("two-hunk diff =\n%q\nwant\n%q", got, wantFirst+wantSecond)
	}
}

func TestUnifiedFromEmpty(t *testing.T) {
	got := Unified("", "hi\n")
	want := "@@ -0,0 +1 @@\n+hi\n"
	if got != want {
		t.Errorf("from-empty diff =\n%q\nwant\n%q", got, want)
	}
}
