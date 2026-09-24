package store

import (
	"context"
	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBillingSelectionAndRevision(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	srv := &domain.Server{Name: "billing"}
	if err := s.CreateServer(ctx, srv); err != nil {
		t.Fatal(err)
	}
	n := networkFixture()
	n.Interfaces[1].Kind = "physical"
	p := agentproto.NetworkBillingPolicy{Mode: "interfaces", InterfaceIDs: []string{n.Interfaces[0].ID, n.Interfaces[1].ID}}
	if err := ValidateBillingInterfaces(n, p); err != nil {
		t.Fatal(err)
	}
	n.Interfaces[0].MasterIndex = 3
	if ValidateBillingInterfaces(n, p) == nil {
		t.Fatal("bridge/slave overlap accepted")
	}
	n.Interfaces[0].MasterIndex = 0
	n.Interfaces[1].Kind = "wireguard"
	if ValidateBillingInterfaces(n, p) == nil {
		t.Fatal("tunnel plus carrier accepted")
	}
	n.Interfaces[1].Kind = "physical"
	n.Status = "incomplete"
	if ValidateBillingInterfaces(n, p) == nil {
		t.Fatal("partial selection accepted")
	}
	n.Status = "ok"
	p.InterfaceIDs = []string{strings.Repeat("f", 32)}
	if ValidateBillingInterfaces(n, p) == nil {
		t.Fatal("unknown interface accepted")
	}
	p.InterfaceIDs = []string{n.Interfaces[0].ID}
	if _, err := s.RequestNetworkBilling(ctx, srv.ID, 0, p); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestNetworkBilling(ctx, srv.ID, 0, p); !errors.Is(err, ErrBillingConflict) {
		t.Fatal("lost update accepted", err)
	}
	v, err := s.NetworkBilling(ctx, srv.ID)
	if err != nil || v.Current.Revision != 0 || v.Requested.Revision != 1 {
		t.Fatal("request applied without agent settlement", v, err)
	}
}

func TestNetworkMigrationsPreserveExistingBillingHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	const beforeNetwork = 11
	for _, migration := range migrations[:beforeNetwork] {
		if _, err = db.Exec(migration); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec("INSERT INTO schema_version(version) VALUES (?)", beforeNetwork); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = db.Exec("INSERT INTO servers(id,name,created_at,updated_at) VALUES (1,'existing',?,?)", stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO traffic_daily(bucket,subject,subject_id,up,down) VALUES (?,'server',1,123,456)", time.Now().UTC().Format("2006-01-02")); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO counter_state(server_id,counter_key,epoch,last_rx,last_tx,updated_at) VALUES (1,'nic','old-boot',1000,2000,?)", stamp); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	view, err := st.NetworkBilling(context.Background(), 1)
	if err != nil || view.Current.Mode != "legacy" || view.Current.Revision != 0 || view.Requested.Revision != 0 {
		t.Fatal(view, err)
	}
	rx, tx, err := st.SumTraffic(context.Background(), SubjectServer, 1, time.Now().Add(-24*time.Hour), time.Now().Add(time.Hour))
	if err != nil || rx != 123 || tx != 456 {
		t.Fatal(rx, tx, err)
	}
	var count int
	if err = st.db.QueryRow("SELECT COUNT(*) FROM counter_state WHERE server_id=1 AND counter_key='nic' AND last_rx=1000 AND last_tx=2000").Scan(&count); err != nil || count != 1 {
		t.Fatal("migration replaced legacy baseline", count, err)
	}
}
