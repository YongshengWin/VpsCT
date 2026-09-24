package proxynode

import "testing"

func TestSubscriptionInfoLabels(t *testing.T) {
	for _, name := range []string{"⌛剩余流量 139.60GB", "📅过期时间 2027-07-10", "剩余流量：1.5 GiB / 200 GB", "Expires: 2027/7/10 12:30:00", "Remaining traffic: 30MB"} {
		if !IsSubscriptionInfo(Proxy{Type: "ss", Name: name}) {
			t.Errorf("metadata not recognized: %s", name)
		}
	}
	for _, name := range []string{"香港 剩余流量 10GB", "剩余流量优化线路", "过期时间 2027-07-10 香港", "到期时间节点", "Remaining traffic route", "ss-hk"} {
		if IsSubscriptionInfo(Proxy{Type: "ss", Name: name}) {
			t.Errorf("real label filtered: %s", name)
		}
	}
	if IsSubscriptionInfo(Proxy{Type: "vless", Name: "剩余流量 10GB"}) {
		t.Fatal("non-dummy protocol filtered")
	}
}
