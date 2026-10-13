package core

import "testing"

func TestVisitValidation(t *testing.T) {
	t.Parallel()
	for _, v := range []Visit{{}, {TimeZone: "America/Chicago", SessionID: "abc", Presentation: "avatar"}, {TimeZone: "UTC", Presentation: "text"}} {
		if err := v.Validate(); err != nil {
			t.Errorf("valid visit %+v: %v", v, err)
		}
	}
	for _, v := range []Visit{{TimeZone: "Local"}, {TimeZone: "not/a/zone"}, {Presentation: "sound"}, {SessionID: "bad\nID"}} {
		if err := v.Validate(); err == nil {
			t.Errorf("accepted invalid visit %+v", v)
		}
	}
}
