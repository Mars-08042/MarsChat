package clientip

import "testing"

func TestResolve(t *testing.T) {
	r, err := NewResolver([]string{"10.0.0.0/8", "192.168.1.5"})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		remote string
		xff    []string
		want   string
	}{
		{name: "no proxy", remote: "203.0.113.7:5000", want: "203.0.113.7"},
		// 直连方不可信时忽略 X-Forwarded-For，防止客户端伪造
		{name: "untrusted remote ignores xff", remote: "203.0.113.7:5000", xff: []string{"1.2.3.4"}, want: "203.0.113.7"},
		{name: "trusted gateway", remote: "10.0.0.2:5000", xff: []string{"198.51.100.9"}, want: "198.51.100.9"},
		// 客户端自己塞了一个假地址在最左边，取最右边第一个不可信的地址
		{name: "spoofed leftmost", remote: "10.0.0.2:5000", xff: []string{"1.1.1.1, 198.51.100.9"}, want: "198.51.100.9"},
		{name: "multiple trusted hops", remote: "10.0.0.2:5000", xff: []string{"198.51.100.9, 192.168.1.5", "10.0.0.3"}, want: "198.51.100.9"},
		{name: "all trusted", remote: "10.0.0.2:5000", xff: []string{"10.0.0.3"}, want: "10.0.0.2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.resolve(tt.remote, tt.xff); got != tt.want {
				t.Errorf("resolve = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewResolverRejectsInvalid(t *testing.T) {
	if _, err := NewResolver([]string{"not-an-ip"}); err == nil {
		t.Fatal("want error for invalid CIDR")
	}
}
