// SPDX-License-Identifier: AGPL-3.0-only
package markdownsource

import (
	"reflect"
	"testing"
)

func TestCodeRangesPreserveEmptyUnclosedAndNestedBlocks(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		lines        []bool
	}{
		{"empty", "~~~\n~~~\n# Real", []bool{true, true, false}},
		{"unclosed", "````\n```\n# Example", []bool{true, true, true}},
		{"long closer", "~~~\n# Example\n~~~~\n# Real", []bool{true, true, true, false}},
		{"indented", "    # Example\n\n# Real", []bool{true, true, false}},
		{"nested list", "- Rule\n\n  ~~~\n  # Example\n  ~~~\n\n# Real", []bool{false, false, true, true, true, false, false}},
		{"crlf", "~~~\r\n# Example\r\n~~~\r\n# Real", []bool{true, true, true, false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := CodeLines(tc.source); !reflect.DeepEqual(got, tc.lines) {
				t.Fatalf("code lines %v want %v", got, tc.lines)
			}
		})
	}
}
