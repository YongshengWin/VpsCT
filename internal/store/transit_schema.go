package store

const transitSchema = `
CREATE TABLE managed_transits (
 id TEXT PRIMARY KEY CHECK(length(id)=32), request_hash TEXT NOT NULL,
 entry_server_id INTEGER NOT NULL, landing_server_id INTEGER NOT NULL,
 entry_node_id INTEGER NOT NULL, landing_node_id INTEGER NOT NULL UNIQUE,
 profile_id INTEGER NOT NULL UNIQUE, stage TEXT NOT NULL,
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX idx_transit_entry ON managed_transits(entry_node_id) WHERE stage<>'retired';
CREATE TRIGGER transit_server_delete BEFORE DELETE ON servers WHEN EXISTS(SELECT 1 FROM managed_transits WHERE stage<>'retired' AND (entry_server_id=OLD.id OR landing_server_id=OLD.id)) BEGIN SELECT RAISE(ABORT,'retire managed transit before deleting server'); END;
CREATE TRIGGER transit_node_delete BEFORE DELETE ON nodes WHEN EXISTS(SELECT 1 FROM managed_transits WHERE stage<>'retired' AND (entry_node_id=OLD.id OR landing_node_id=OLD.id)) BEGIN SELECT RAISE(ABORT,'retire managed transit before deleting node'); END;
CREATE TRIGGER transit_wg_insert AFTER INSERT ON nodes WHEN NEW.source='transit' BEGIN
 INSERT INTO server_network_requirements(server_id,binding_version,egress_version,wireguard_version) VALUES(NEW.server_id,1,1,1)
 ON CONFLICT(server_id) DO UPDATE SET wireguard_version=1,egress_version=1;
 INSERT INTO server_network_generations(server_id,generation) VALUES(NEW.server_id,1) ON CONFLICT(server_id) DO UPDATE SET generation=generation+1;
END;
`
