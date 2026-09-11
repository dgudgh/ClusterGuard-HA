package redact

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBoundedPreservesUTF8AndRedaction(t *testing.T) {
	for _, input := range []string{"abc", "\u4e3b\u5e93\u6821\u9a8c\u5931\u8d25", "A\u00e9\U0001f512\u4e2dZ", "password=private \u5931\u8d25", "bad\xff\u4e2d"} {
		for limit := 1; limit <= len(input)+1; limit++ {
			got := Bounded(input, limit)
			if len(got) > limit || !utf8.ValidString(got) || strings.Contains(got, "private") {
				t.Errorf("input=%q limit=%d output=%q", input, limit, got)
			}
		}
	}
}
