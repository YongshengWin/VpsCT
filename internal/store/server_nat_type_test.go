package store

import (
	"context"
	"ctlvps/internal/natconfig"
	"database/sql"
	"errors"
	"testing"
	"time"

	"ctlvps/internal/domain"
)

func TestDeclaredNATRequiresMappingAndPersistsType(t *testing.T) {
	ctx := context.Background()
	s, srv := natStoreFixture(t)
	srv.IsNAT = true
	if err := s.UpdateServer(ctx, &srv); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetServer(ctx, srv.ID)
	if err != nil || !got.IsNAT {
		t.Fatalf("type lost: %+v %v", got, err)
	}
	if _, err := s.NATPortConfig(ctx, srv.ID); !errors.Is(err, ErrNATMappingRequired) {
		t.Fatalf("unmapped NAT allowed random allocation: %v", err)
	}
	if _, err := s.SaveServerNAT(ctx, srv.ID, 0, natFixtureConfig()); err != nil {
		t.Fatal(err)
	}
	natFixtureApplied(t, s, srv.ID, natFixtureConfig(), "controller")
	if c, err := s.NATPortConfig(ctx, srv.ID); err != nil || c == nil {
		t.Fatalf("applied NAT not usable: %v", err)
	}
	srv.IsNAT = false
	if err := s.UpdateServer(ctx, &srv); !errors.Is(err, ErrNATResources) {
		t.Fatalf("active mapping hidden by type change: %v", err)
	}
}

func TestCreateNATTypeDefaultsAndRoundTrip(t *testing.T) {
	s := openTest(t)
	for _, declared := range []bool{false, true} {
		v := domain.Server{Name: map[bool]string{false: "public", true: "nat"}[declared], IsNAT: declared}
		if err := s.CreateServer(context.Background(), &v); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetServer(context.Background(), v.ID)
		if err != nil || got.IsNAT != declared {
			t.Fatalf("type round trip: %+v %v", got, err)
		}
	}
}

func TestNATTypeMigrationPreservesExistingMappings(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE servers(id INTEGER PRIMARY KEY); CREATE TABLE agents(server_id INTEGER,diagnostics TEXT); CREATE TABLE server_nat_configs(server_id INTEGER,config TEXT);
 INSERT INTO servers VALUES(1),(2),(3),(4);
 INSERT INTO server_nat_configs VALUES(2,'{"schema":1,"mappings":[{"public_port":30001,"local_port":10001}]}');
 INSERT INTO agents VALUES(1,'{"nat":{"version":2,"source":"none","configured":false}}'),(3,'{"nat":{"source":"local","configured":true}}'),(4,'{"nat":{"source":"local","status":"error"}}');`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(migrations[36]); err != nil {
		t.Fatal(err)
	}
	for id := 1; id <= 4; id++ {
		var n int
		if err := db.QueryRow(`SELECT is_nat FROM servers WHERE id=?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		want := 1
		if id == 1 {
			want = 0
		}
		if n != want {
			t.Fatalf("server %d type=%d want=%d", id, n, want)
		}
	}
}

func TestInitialNATFailureRollsBackServer(t *testing.T) {
	ctx := context.Background()
	s, _ := natStoreFixture(t)
	if _, err := s.DB().Exec(`CREATE TRIGGER reject_initial_nat BEFORE INSERT ON server_nat_configs BEGIN SELECT RAISE(ABORT,'fixture write failure'); END;`); err != nil {
		t.Fatal(err)
	}
	srv := domain.Server{Name: "atomic-nat", IsNAT: true, PublicHost: "nat.example.com", Enabled: true}
	cfg := natconfig.Config{Schema: 1, Mappings: []natconfig.Mapping{{PublicPort: 30001, LocalPort: 10001, Network: "tcp"}}}
	if err := s.CreateServerWithNATAndCalibration(ctx, &srv, &cfg, nil, time.Time{}); err == nil {
		t.Fatal("expected write failure")
	}
	var count int
	if err := s.DB().QueryRow(`SELECT count(*) FROM servers WHERE name='atomic-nat'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("server survived rollback: %d %v", count, err)
	}
}
