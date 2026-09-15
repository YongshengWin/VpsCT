package connlog

import (
	"context"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
)

func TestMigrateAddsSrcHost(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.db.Exec(`DROP TABLE conn_events`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TABLE conn_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT, server_id INTEGER, node_id INTEGER, share_id INTEGER,
		ts TEXT, network TEXT, dest_host TEXT, dest_port INTEGER, agent_seq INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := migrateConnEvents(s.db); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('conn_events') WHERE name='src_host'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("src_host missing after migrate: %d %v", n, err)
	}
}

func TestIngestSelfAndSummary(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	shareID := int64(9)
	n, err := s.Ingest(context.Background(), 1, agentproto.ConnlogBatch{Seq: 1, Events: []agentproto.ConnEvent{
		{TS: now, NodeID: 2, DestHost: "a.example", DestPort: 443, Network: "tcp", SrcHost: "1.1.1.1"},
		{TS: now, NodeID: 3, DestHost: "b.example", DestPort: 443, Network: "tcp", SrcHost: "2.2.2.2"},
	}}, map[int64]*int64{3: &shareID})
	if err != nil || n != 2 {
		t.Fatalf("ingest %d %v", n, err)
	}
	selfN, err := s.Count(context.Background(), Filter{SelfOnly: true})
	if err != nil || selfN != 1 {
		t.Fatalf("self %d %v", selfN, err)
	}
	clients, err := s.GroupCount(context.Background(), Filter{}, "src_host", 10)
	if err != nil || len(clients) != 2 {
		t.Fatalf("clients %v %v", clients, err)
	}
	hosts, err := s.GroupCount(context.Background(), Filter{ServerID: int64ptr(1)}, "dest_host", 10)
	if err != nil || len(hosts) != 2 {
		t.Fatalf("hosts %v %v", hosts, err)
	}
}

func int64ptr(n int64) *int64 { return &n }
