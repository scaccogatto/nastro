package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestMetaCommand(t *testing.T) {
	tests := []struct {
		args        []string
		wantHandled bool
		wantOut     string
	}{
		{[]string{"help"}, true, "usage:"},
		{[]string{"-h"}, true, "usage:"},
		{[]string{"--help"}, true, "usage:"},
		{[]string{"version"}, true, "nastro dev"},
		{[]string{"--version"}, true, "nastro dev"},
		{[]string{"-v"}, true, "nastro dev"},
		{nil, false, ""},
		{[]string{"record"}, false, ""},
		{[]string{"bogus"}, false, ""},
	}
	for _, tt := range tests {
		var out bytes.Buffer
		if got := metaCommand(tt.args, &out); got != tt.wantHandled {
			t.Errorf("metaCommand(%q) handled = %v, want %v", tt.args, got, tt.wantHandled)
		}
		if !strings.Contains(out.String(), tt.wantOut) {
			t.Errorf("metaCommand(%q) output = %q, want it to contain %q", tt.args, out.String(), tt.wantOut)
		}
		if !tt.wantHandled && out.Len() != 0 {
			t.Errorf("metaCommand(%q) wrote %q for an unhandled command", tt.args, out.String())
		}
	}
}
