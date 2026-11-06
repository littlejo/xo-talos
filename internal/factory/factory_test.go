package factory

import "testing"

func TestResolveVersion(t *testing.T) {
	versions := []string{
		"v1.9.6", "v1.10.0", "v1.13.0", "v1.13.9", "v1.13.10", "v1.14.0",
		"v1.14.5", "v1.15.0-alpha.0", "v1.15.0-alpha.2", "latest", "v1.12",
	}
	tests := []struct {
		want, got string
		ok        bool
	}{
		{"", "v1.14.5", true},
		{"latest", "v1.14.5", true},
		{"v1.13", "v1.13.10", true}, // numeric, not lexical
		{"1.13", "v1.13.10", true},
		{"v1.9", "v1.9.6", true},
		{"v1.1", "", false},  // must not match v1.10.x or v1.13.x
		{"v1.15", "", false}, // only pre-releases
		{"v1.14.5", "v1.14.5", true},
		{"1.14.0", "v1.14.0", true},
		{"v1.15.0-alpha.2", "v1.15.0-alpha.2", true},
		{"v1.14.99", "", false},
	}
	for _, tt := range tests {
		got, err := ResolveVersion(versions, tt.want)
		if got != tt.got || (err == nil) != tt.ok {
			t.Errorf("ResolveVersion(%q) = %q, %v; want %q (ok=%v)", tt.want, got, err, tt.got, tt.ok)
		}
	}
	if _, err := ResolveVersion([]string{"v1.15.0-rc.1"}, ""); err == nil {
		t.Error("found a stable version among pre-releases only")
	}
}
