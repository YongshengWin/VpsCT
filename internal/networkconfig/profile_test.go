package networkconfig

import "testing"

func TestDirectConfigIsTypedAndAdvertiseHostIsAnEndpoint(t *testing.T) {
	for _, raw := range []string{
		`{"family":"ipv4","dns":{"transport":"udp","address":"192.0.2.53","port":53},"arbitrary_core_option":true}`,
		`{"family":"ipv4","dns":{"transport":"udp","address":"192.0.2.53","port":53,"detour":"other"}}`,
		`null`, `{} {}`,
	} {
		if _, err := DecodeDirect([]byte(raw)); err == nil {
			t.Fatal("unsupported direct config accepted", raw)
		}
	}
	for _, host := range []string{"example.test", "192.0.2.1", "2001:db8::1"} {
		if err := ValidateAdvertiseHost(host); err != nil {
			t.Fatal(host, err)
		}
	}
	for _, host := range []string{"", "https://example.test", "example.test:443", "0.0.0.0", "::", "fe80::1%eth0", "example.test/path", "a\nb.test"} {
		if ValidateAdvertiseHost(host) == nil {
			t.Fatal("invalid endpoint accepted", host)
		}
	}
}
