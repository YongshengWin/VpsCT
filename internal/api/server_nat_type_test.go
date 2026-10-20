package api

import (
	"context"
	"ctlvps/internal/domain"
	"testing"
)

func TestServerEditPreservesNATTypeWhenOmitted(t *testing.T) {
	s := domain.Server{Name: "nat", IsNAT: true}
	in := serverInput{Name: "edited"}
	if err := in.apply(&s); err != nil {
		t.Fatal(err)
	}
	if !s.IsNAT {
		t.Fatal("legacy client edit erased NAT type")
	}
	explicit := false
	in.IsNAT = &explicit
	if err := in.apply(&s); err != nil {
		t.Fatal(err)
	}
	if s.IsNAT {
		t.Fatal("explicit network type edit ignored")
	}
}

func TestCreateServerWithOptionalNATMapping(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	cfg := map[string]any{"schema": 1, "mappings": []any{map[string]any{"public_port": 30001, "local_port": 10001, "network": "tcp"}}}
	created := c.do("POST", "/api/v1/servers", map[string]any{"name": "nat-mapped", "public_host": "nat.example.com", "is_nat": true, "nat_config": cfg}, 201)
	sid := int64(created["id"].(float64))
	v, err := c.api.Store.ServerNAT(context.Background(), sid)
	if err != nil || v.DesiredConfig == nil || v.Revision != 1 || !v.Pending || v.Editable {
		t.Fatalf("initial NAT intent: %#v, %v", v, err)
	}
	if v.DesiredConfig.Mappings[0].PublicPort != 30001 || v.DesiredConfig.Mappings[0].LocalPort != 10001 {
		t.Fatal("mapping lost")
	}
	skipped := c.do("POST", "/api/v1/servers", map[string]any{"name": "nat-skipped", "is_nat": true}, 201)
	v, err = c.api.Store.ServerNAT(context.Background(), int64(skipped["id"].(float64)))
	if err != nil || v.DesiredConfig != nil || v.Revision != 0 {
		t.Fatalf("skip created NAT intent: %#v %v", v, err)
	}
	c.do("POST", "/api/v1/servers", map[string]any{"name": "public-with-mapping", "nat_config": cfg}, 400)
	c.do("POST", "/api/v1/servers", map[string]any{"name": "invalid-mapping", "is_nat": true, "nat_config": map[string]any{"schema": 1, "mappings": []any{map[string]any{"public_port": 65536, "local_port": 10001}}}}, 400)
	var count int
	if err := c.api.Store.DB().QueryRow(`SELECT count(*) FROM servers`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("invalid submission left a server: count=%d err=%v", count, err)
	}
}
