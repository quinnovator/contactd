package core

import (
	"errors"
	"fmt"
	"strings"
	"time"
	_ "time/tzdata" // Named zones work in minimal daemon images too.
)

// Visit describes the person's connection. Empty fields preserve legacy API use.
type Visit struct {
	SessionID    string `json:"session_id,omitempty"`
	TimeZone     string `json:"time_zone,omitempty"`
	Presentation string `json:"presentation,omitempty"` // "avatar" or "text"
}

func (v Visit) Validate() error {
	if len(v.SessionID) > 128 || strings.ContainsAny(v.SessionID, "\x00\r\n") {
		return errors.New("invalid session_id")
	}
	if v.TimeZone != "" {
		if v.TimeZone == "Local" {
			return errors.New("time_zone must name an IANA time zone")
		}
		if _, err := time.LoadLocation(v.TimeZone); err != nil {
			return fmt.Errorf("invalid time_zone: %w", err)
		}
	}
	if v.Presentation != "" && v.Presentation != "avatar" && v.Presentation != "text" {
		return errors.New("presentation must be avatar or text")
	}
	return nil
}
