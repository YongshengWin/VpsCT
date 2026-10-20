package openrc

import "testing"

func TestRefreshReceiptRejectsForeignRestart(t *testing.T) {
	for _, body := range []string{
		`{"version":1,"units":["sshd.service"]}`,
		`{"version":1,"units":["ctlvps-proxy-guard.service"]}`,
		`{"version":1,"units":["ctlvps-singbox.service","ctlvps-singbox.service"]}`,
		`{"version":0,"units":[]}`,
		`{"version":1,"units":[],"command":"id"}`,
		`{"version":1,"units":[]} {}`,
	} {
		if _, err := decodeRefresh([]byte(body)); err == nil {
			t.Fatalf("accepted invalid helper receipt: %s", body)
		}
	}
	if _, err := decodeRefresh([]byte(`{"version":1,"units":["ctlvps-singbox.service","ctlvps-singbox-private.service"]}`)); err != nil {
		t.Fatal(err)
	}
}
