package store

const accessRouteSchema = `
CREATE TABLE access_routes (
 id TEXT PRIMARY KEY CHECK(length(id)=32),
 request_hash TEXT NOT NULL,
 name TEXT NOT NULL,
 request TEXT NOT NULL CHECK(json_valid(request)),
 entry_server_id INTEGER REFERENCES servers(id) ON DELETE RESTRICT,
 ix_server_id INTEGER REFERENCES servers(id) ON DELETE RESTRICT,
 landing_node_id INTEGER REFERENCES nodes(id) ON DELETE RESTRICT,
 entry_forward_id INTEGER NOT NULL,
 ix_forward_id INTEGER NOT NULL,
 subscription_id INTEGER REFERENCES subscriptions(id) ON DELETE SET NULL,
 stage TEXT NOT NULL CHECK(stage IN ('pending_landing','pending_ix','pending_entry','applied','blocked','stopping_entry','stopping_ix','retired')),
 message TEXT NOT NULL DEFAULT '',
 revision INTEGER NOT NULL DEFAULT 1,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE INDEX idx_access_routes_entry ON access_routes(entry_server_id);
CREATE INDEX idx_access_routes_ix ON access_routes(ix_server_id);
CREATE INDEX idx_access_routes_landing ON access_routes(landing_node_id);
CREATE UNIQUE INDEX idx_access_routes_entry_forward ON access_routes(entry_forward_id);
CREATE UNIQUE INDEX idx_access_routes_ix_forward ON access_routes(ix_forward_id);
`

// The relay forward is optional. Keep v40 immutable so upgrades preserve all
// existing routes, ownership indexes, and pending/retiring lifecycle states.
const optionalAccessRelaySchema = `
CREATE TABLE access_routes_optional (
 id TEXT PRIMARY KEY CHECK(length(id)=32),
 request_hash TEXT NOT NULL,
 name TEXT NOT NULL,
 request TEXT NOT NULL CHECK(json_valid(request)),
 entry_server_id INTEGER REFERENCES servers(id) ON DELETE RESTRICT,
 ix_server_id INTEGER REFERENCES servers(id) ON DELETE RESTRICT,
 landing_node_id INTEGER REFERENCES nodes(id) ON DELETE RESTRICT,
 entry_forward_id INTEGER NOT NULL,
 ix_forward_id INTEGER,
 subscription_id INTEGER REFERENCES subscriptions(id) ON DELETE SET NULL,
 stage TEXT NOT NULL CHECK(stage IN ('pending_landing','pending_ix','pending_entry','applied','blocked','stopping_entry','stopping_ix','retired')),
 message TEXT NOT NULL DEFAULT '',
 revision INTEGER NOT NULL DEFAULT 1,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
INSERT INTO access_routes_optional SELECT * FROM access_routes;
DROP TABLE access_routes;
ALTER TABLE access_routes_optional RENAME TO access_routes;
CREATE INDEX idx_access_routes_entry ON access_routes(entry_server_id);
CREATE INDEX idx_access_routes_ix ON access_routes(ix_server_id);
CREATE INDEX idx_access_routes_landing ON access_routes(landing_node_id);
CREATE UNIQUE INDEX idx_access_routes_entry_forward ON access_routes(entry_forward_id);
CREATE UNIQUE INDEX idx_access_routes_ix_forward ON access_routes(ix_forward_id);
`
