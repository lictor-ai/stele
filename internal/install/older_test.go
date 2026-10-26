package install

import "testing"

func TestOlder(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"v1.0.0", "v1.0.1", true},
		{"v1.2.0", "v1.10.0", true},
		{"v1.0.0-dev.2", "v1.0.0", true},
		{"v1.0.0-dev.2", "v1.0.0-dev.10", true},
		{"v1.0.0-alpha", "v1.0.0-beta", true},
		{"v1.0.0-1", "v1.0.0-a", true},
		{"v1.0.0-a", "v1.0.0-a.1", true},
		{"v1.0.0", "v1.0.0", false},
		{"v2.0.0", "v1.9.9", false},
		{"v1.0.1", "v1.0.0", false},
		{"v1.0.0", "v1.0.0-dev.2", false},
		{"v1.0.0-a.1", "v1.0.0-a", false},
	}
	for _, tt := range tests {
		if got := Older(tt.a, tt.b); got != tt.want {
			t.Errorf("Older(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}
