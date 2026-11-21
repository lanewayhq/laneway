package rootdomain

import "testing"

func TestCoversTheRootDomainAndNamesUnderItOnly(t *testing.T) {
	for host, want := range map[string]bool{
		"lw.example.com":                   true,
		"shop.lw.example.com":              true,
		"vite.feature.shop.lw.example.com": true,
		"example.com":                      false,
		"shoplw.example.com":               false,
		"shop.localhost":                   false,
	} {
		if got := Covers("lw.example.com", host); got != want {
			t.Errorf("Covers(lw.example.com, %s) = %v, want %v", host, got, want)
		}
	}
}
