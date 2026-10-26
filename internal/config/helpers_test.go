package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/config"
)

func TestHelperConfig(t *testing.T) {
	t.Parallel()
	c, err := config.Load([]string{"-llm=fake"}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if c.Helpers != "on" || c.A2AAuth != "token" || c.HelpersMinEvery != time.Minute || c.HelpersMaxActive != 16 || c.HelpersPerContact != 4 || c.HelpersConcurrency != 4 || c.A2ABaseURL() != "http://127.0.0.1:8080" {
		t.Fatalf("defaults: %+v", c)
	}
	for _, tc := range []struct{ addr, want string }{{":9000", "http://127.0.0.1:9000"}, {"localhost:9001", "http://localhost:9001"}, {"[::1]:8000", "http://[::1]:8000"}} {
		c.Addr = tc.addr
		if got := c.A2ABaseURL(); got != tc.want {
			t.Fatalf("base: %s", got)
		}
	}
	for _, flag := range []string{"-helpers=bad", "-a2a-auth=bad", "-a2a-base-url=relative", "-helper-agent-url=ftp://host", "-helpers-min-every=0", "-helpers-min-every=25h", "-helpers-max-active=0", "-helpers-per-contact=0", "-helpers-concurrency=0", "-helper-tools=shell", "-fetch-allow=localhost", "-helper-effort=bad"} {
		if _, err := config.Load([]string{"-llm=fake", flag}, func(string) string { return "" }); err == nil {
			t.Fatalf("accepted %s", flag)
		}
	}
	c, err = config.Load([]string{"-llm=fake"}, func(k string) string {
		if k == "CONTACTD_A2A_BASE_URL" {
			return "https://helpers.example/"
		}
		return ""
	})
	if err != nil || strings.HasSuffix(c.A2ABaseURL(), "/") {
		t.Fatalf("env URL: %s %v", c.A2ABaseURL(), err)
	}
}
