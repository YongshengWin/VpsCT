package api

import (
	"context"
	"encoding/json"
	"testing"
)

func TestAPIKeyAuditPaginationBeyondTenThousandDoesNotRepeat(t *testing.T) {
	c := setupAPIKeyTest(t)
	key, _ := issueTestAPIKey(t, c, 1, "read")
	_, err := c.api.Store.DB().ExecContext(context.Background(), `WITH RECURSIVE sequence(n) AS (
		SELECT 1 UNION ALL SELECT n+1 FROM sequence WHERE n<10002
	) INSERT INTO audit_log(ts,action,target) SELECT '2026-01-01T00:00:00Z','fixture',CAST(n AS TEXT) FROM sequence`)
	if err != nil {
		t.Fatal(err)
	}
	_, first := keyRequest(t, c, "GET", "/api/v1/audit?limit=1&offset=10000", key, nil, nil, 200)
	// Each developer request adds an audit row before reading. Advance past
	// that new row as well as the previous result to compare the stable fixture.
	_, next := keyRequest(t, c, "GET", "/api/v1/audit?limit=1&offset=10002", key, nil, nil, 200)
	var firstRows, nextRows []map[string]any
	if err := json.Unmarshal(first, &firstRows); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(next, &nextRows); err != nil {
		t.Fatal(err)
	}
	if len(firstRows) != 1 || len(nextRows) != 1 {
		t.Fatal("pagination omitted available rows")
	}
	firstID := firstRows[0]["id"].(float64)
	nextID := nextRows[0]["id"].(float64)
	if firstID-nextID != 1 {
		t.Fatalf("deep pagination repeated or skipped a row: %v, %v", firstID, nextID)
	}
}
