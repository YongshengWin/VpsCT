package geoip

import (
	"errors"
	"net"
	"net/http"
	"testing"
)

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOnlineLookupsRequireOptIn(t *testing.T) {
	l := Open(t.TempDir())
	requests := 0
	l.http = &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("test transport: no network")
	})}
	l.Find("8.8.8.8")
	if requests != 0 {
		t.Fatal("default lookup disclosed an IP to an external service")
	}
	l.SetOnlineEnabled(true)
	l.Find("1.1.1.1")
	if requests == 0 {
		t.Fatal("opt-in did not enable online lookups")
	}
	before := requests
	l.SetOnlineEnabled(false)
	l.Find("9.9.9.9")
	if requests != before {
		t.Fatal("lookup sent a request after opt-out")
	}
}

func TestParseRegion(t *testing.T) {
	cases := map[string]string{
		"中国|北京|北京市|联通|CN":                                    "北京 · 联通",
		"中国|浙江省|杭州市|电信|CN":                                   "浙江 杭州 · 电信",
		"中国|上海|上海市||CN":                                      "上海",
		"中国|浙江省|杭州市|移动|CN":                                   "浙江 · 移动",
		"中国|浙江省|杭州市|中国移动|CN":                                 "浙江 · 移动",
		"中国|广东省|深圳市|广电|CN":                                   "广东 · 广电",
		"中国|0|0|电信|CN":                                       "",
		"中国|北京|0|电信|CN":                                      "北京 · 电信",
		"内网IP|0|0|内网IP|0":                                    "",
		"0|0|0|0|0":                                          "",
		"United States|California|Los Angeles|Google LLC|US": "United States California · Google LLC",
		"美国|加利福尼亚|0|0|US":                                    "美国 加利福尼亚",
		"":                                                   "",
	}
	for in, want := range cases {
		if got := ParseRegion(in).Label; got != want {
			t.Fatalf("%q: got %q want %q", in, got, want)
		}
	}
	if ParseRegion("中国|浙江省|杭州市|移动|CN").City != "" {
		t.Fatal("mobile city must be dropped")
	}
	if ParseRegion("中国|浙江省|杭州市|电信|CN").City != "杭州" {
		t.Fatal("telecom city should stay")
	}
}

func TestFindWithoutDB(t *testing.T) {
	l := Open(t.TempDir())
	l.disableOnline = true
	if l.Find("1.2.3.4") != nil || l.Find("not-an-ip") != nil || l.Find("192.168.1.1") != nil || l.Find("10.0.0.8") != nil {
		t.Fatal("missing db / private ip must not invent a city")
	}
	if l.Find("fd00::1") != nil || l.Find("fe80::1") != nil || l.Find("::1") != nil || l.Find("2001:db8::1") != nil {
		t.Fatal("missing db / private ipv6 must not invent a city")
	}
}

func TestPublicIP(t *testing.T) {
	if publicIP(net.ParseIP("8.8.8.8")) != true || publicIP(net.ParseIP("2001:4860:4860::8888")) != true {
		t.Fatal("public addresses should pass")
	}
	if publicIP(net.ParseIP("10.0.0.1")) || publicIP(net.ParseIP("192.168.1.1")) || publicIP(net.ParseIP("fd12:3456::1")) {
		t.Fatal("private addresses should be skipped")
	}
}

func TestParsePconline(t *testing.T) {
	got := parsePconline(pconlineResp{Pro: "北京市", City: "北京市", Region: "朝阳区", Addr: "北京市朝阳区 电信"})
	if got.Label != "北京 朝阳 · 电信" {
		t.Fatalf("beijing: %q", got.Label)
	}
	got = parsePconline(pconlineResp{Pro: "浙江省", City: "温州市", Addr: "浙江省温州市 移通"})
	if got.Label != "浙江 温州 · 移动" {
		t.Fatalf("wenzhou mobile: %q", got.Label)
	}
	got = parsePconline(pconlineResp{Addr: "美国"})
	if got.Label != "美国" {
		t.Fatalf("us: %q", got.Label)
	}
}
