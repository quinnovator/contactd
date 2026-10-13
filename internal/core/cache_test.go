package core_test

import (
	"encoding/json"
	"testing"

	"github.com/quinnovator/contactd/internal/core"
)

func TestAttemptCacheReportCompatibility(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, record string
		reported     bool
	}{
		{"legacy positive", `{"input":1000,"cached":768}`, true},
		{"legacy zero is ambiguous", `{"input":1000,"cached":0}`, false},
		{"explicit cold cache", `{"input":1000,"cached":0,"cache_reported":true}`, true},
		{"missing usage", `{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var a core.AttemptBody
			if err := json.Unmarshal([]byte(tc.record), &a); err != nil {
				t.Fatal(err)
			}
			if a.HasCacheReport() != tc.reported {
				t.Errorf("HasCacheReport() = %v, want %v", a.HasCacheReport(), tc.reported)
			}
		})
	}
}
