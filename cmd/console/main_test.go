package main

import "testing"

func TestValidateBroker(t *testing.T) {
	ok := config{
		provider: "google", redirectURL: "https://console.example.com/auth/callback",
		allowedDomains: []string{"example.com"}, brokerEnabled: true,
		crewNamespace: "kiro-crews", tailnetDomain: "tailnet.example.com",
	}
	tests := []struct {
		name    string
		mutate  func(*config)
		wantErr bool
	}{
		{"valid", func(*config) {}, false},
		{"broker off needs neither", func(c *config) {
			c.brokerEnabled, c.crewNamespace, c.tailnetDomain = false, "", ""
		}, false},
		{"empty namespace", func(c *config) { c.crewNamespace = "" }, true},
		{"empty domain", func(c *config) { c.tailnetDomain = "" }, true},
		{"single label", func(c *config) { c.tailnetDomain = "net" }, true},
		{"leading dot", func(c *config) { c.tailnetDomain = ".tailnet.example.com" }, true},
		{"trailing dot", func(c *config) { c.tailnetDomain = "tailnet.example.com." }, true},
		{"empty label", func(c *config) { c.tailnetDomain = "tailnet..example.com" }, true},
		{"slash", func(c *config) { c.tailnetDomain = "tailnet.example.com/x" }, true},
		{"two labels", func(c *config) { c.tailnetDomain = "example.com" }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := ok
			tt.mutate(&c)
			if err := c.validate(); (err != nil) != tt.wantErr {
				t.Fatalf("validate() = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
