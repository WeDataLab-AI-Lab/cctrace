package airuntime

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateText_KeepsInvalidUTF8(t *testing.T) {
	if got := TruncateText(strings.Repeat("\x80", 100), 10, "|"); len(got) < 10-utf8.UTFMax {
		t.Errorf("cut %d bytes of non-UTF-8 input, want about 10", len(got))
	}
}
