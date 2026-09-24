package store

// Existing duplicate legacy node ports remain visible; migration must not
// discard either owner. New reservations cannot overlap any existing owner.
// Disabled and retiring resources retain their reservations until cleanup ACK.
const forwardSchema = `
CREATE TABLE server_listener_reservations (
 server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
 resource_kind TEXT NOT NULL CHECK(resource_kind IN ('node','forward','transit')),
 resource_id INTEGER NOT NULL CHECK(resource_id>0),
 listen_port INTEGER NOT NULL CHECK(listen_port BETWEEN 1 AND 65535),
 PRIMARY KEY(resource_kind,resource_id,listen_port)
);
CREATE INDEX idx_listener_reservation_port ON server_listener_reservations(server_id,listen_port);
INSERT INTO server_listener_reservations(server_id,resource_kind,resource_id,listen_port)
 SELECT server_id,'node',id,listen_port FROM nodes WHERE server_id IS NOT NULL AND listen_port BETWEEN 1 AND 65535;
CREATE TRIGGER listener_reservation_conflict BEFORE INSERT ON server_listener_reservations
 WHEN EXISTS(SELECT 1 FROM server_listener_reservations WHERE server_id=NEW.server_id AND listen_port=NEW.listen_port
 AND (resource_kind<>NEW.resource_kind OR resource_id<>NEW.resource_id)) BEGIN
 SELECT RAISE(ABORT,'listener port already reserved');
END;
CREATE TRIGGER listener_reservation_immutable BEFORE UPDATE ON server_listener_reservations BEGIN
 SELECT RAISE(ABORT,'listener reservations are immutable');
END;
CREATE TRIGGER listener_node_insert AFTER INSERT ON nodes WHEN NEW.server_id IS NOT NULL AND NEW.listen_port BETWEEN 1 AND 65535 BEGIN
 INSERT INTO server_listener_reservations VALUES(NEW.server_id,'node',NEW.id,NEW.listen_port);
END;
CREATE TRIGGER listener_node_update AFTER UPDATE OF server_id,listen_port ON nodes
 WHEN OLD.server_id IS NOT NEW.server_id OR OLD.listen_port IS NOT NEW.listen_port BEGIN
 DELETE FROM server_listener_reservations WHERE resource_kind='node' AND resource_id=OLD.id;
 INSERT INTO server_listener_reservations SELECT NEW.server_id,'node',NEW.id,NEW.listen_port
 WHERE NEW.server_id IS NOT NULL AND NEW.listen_port BETWEEN 1 AND 65535;
END;
CREATE TRIGGER listener_node_delete AFTER DELETE ON nodes BEGIN
 DELETE FROM server_listener_reservations WHERE resource_kind='node' AND resource_id=OLD.id;
END;

CREATE TABLE port_forwards (
 id INTEGER PRIMARY KEY AUTOINCREMENT CHECK(id BETWEEN 1 AND 16777215),
 server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
 name TEXT NOT NULL CHECK(length(name) BETWEEN 1 AND 128),
 config TEXT NOT NULL CHECK(json_valid(config)),
 listen_port INTEGER NOT NULL CHECK(listen_port BETWEEN 1 AND 65535),
 egress_profile_id INTEGER, egress_revision INTEGER,
 enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
 revision INTEGER NOT NULL CHECK(revision BETWEEN 1 AND 4096),
 retired INTEGER NOT NULL DEFAULT 0 CHECK(retired IN (0,1)),
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 CHECK(retired=0 OR enabled=0),
 CHECK(json_extract(config,'$.listen_port')=listen_port),
 CHECK((egress_profile_id IS NULL)=(egress_revision IS NULL)),
 CHECK(COALESCE(json_extract(config,'$.egress_profile_id'),0)=COALESCE(egress_profile_id,0)),
 CHECK(COALESCE(json_extract(config,'$.egress_revision'),0)=COALESCE(egress_revision,0)),
 FOREIGN KEY(egress_profile_id,egress_revision,server_id) REFERENCES egress_profile_revisions(profile_id,revision,server_id) DEFERRABLE INITIALLY DEFERRED
);
CREATE INDEX idx_port_forwards_server ON port_forwards(server_id,id);
CREATE INDEX idx_port_forwards_profile ON port_forwards(egress_profile_id,egress_revision);
CREATE TABLE port_forward_revisions (
 forward_id INTEGER NOT NULL REFERENCES port_forwards(id) ON DELETE CASCADE,
 revision INTEGER NOT NULL, name TEXT NOT NULL, config TEXT NOT NULL,
 enabled INTEGER NOT NULL, retired INTEGER NOT NULL, created_at TEXT NOT NULL,
 PRIMARY KEY(forward_id,revision)
);
CREATE TRIGGER forward_revision_immutable BEFORE UPDATE ON port_forward_revisions BEGIN
 SELECT RAISE(ABORT,'forward revisions are immutable');
END;
CREATE TRIGGER forward_identity_immutable BEFORE UPDATE OF id,server_id ON port_forwards
 WHEN OLD.id<>NEW.id OR OLD.server_id<>NEW.server_id BEGIN
 SELECT RAISE(ABORT,'forward identity is immutable');
END;
CREATE TRIGGER listener_forward_insert AFTER INSERT ON port_forwards BEGIN
 INSERT INTO server_listener_reservations VALUES(NEW.server_id,'forward',NEW.id,NEW.listen_port);
 INSERT INTO port_forward_revisions VALUES(NEW.id,NEW.revision,NEW.name,NEW.config,NEW.enabled,NEW.retired,NEW.updated_at);
END;
CREATE TRIGGER listener_forward_update AFTER UPDATE OF listen_port ON port_forwards WHEN OLD.listen_port<>NEW.listen_port BEGIN
 INSERT INTO server_listener_reservations VALUES(NEW.server_id,'forward',NEW.id,NEW.listen_port) ON CONFLICT DO NOTHING;
END;
CREATE TRIGGER forward_revision_update AFTER UPDATE ON port_forwards WHEN OLD.revision<>NEW.revision BEGIN
 INSERT INTO port_forward_revisions VALUES(NEW.id,NEW.revision,NEW.name,NEW.config,NEW.enabled,NEW.retired,NEW.updated_at);
END;
CREATE TRIGGER listener_forward_delete AFTER DELETE ON port_forwards BEGIN
 DELETE FROM server_listener_reservations WHERE resource_kind='forward' AND resource_id=OLD.id;
END;
ALTER TABLE server_network_requirements ADD COLUMN forward_version INTEGER NOT NULL DEFAULT 0 CHECK(forward_version IN (0,1));
CREATE TRIGGER forward_generation_insert AFTER INSERT ON port_forwards BEGIN
 INSERT INTO server_network_requirements(server_id,binding_version,forward_version) VALUES(NEW.server_id,1,1)
 ON CONFLICT(server_id) DO UPDATE SET forward_version=1;
 INSERT INTO server_network_generations(server_id,generation) VALUES(NEW.server_id,1) ON CONFLICT(server_id) DO UPDATE SET generation=generation+1;
END;
CREATE TRIGGER forward_generation_update AFTER UPDATE ON port_forwards BEGIN
 INSERT INTO server_network_generations(server_id,generation) VALUES(NEW.server_id,1) ON CONFLICT(server_id) DO UPDATE SET generation=generation+1;
END;
CREATE TRIGGER forward_generation_delete AFTER DELETE ON port_forwards BEGIN
 INSERT INTO server_network_generations(server_id,generation) SELECT OLD.server_id,1 WHERE EXISTS(SELECT 1 FROM servers WHERE id=OLD.server_id)
 ON CONFLICT(server_id) DO UPDATE SET generation=generation+1;
END;
CREATE TRIGGER forward_generation_profile AFTER UPDATE OF enabled ON egress_profiles WHEN OLD.enabled<>NEW.enabled
 AND EXISTS(SELECT 1 FROM port_forwards WHERE egress_profile_id=NEW.id) BEGIN
 INSERT INTO server_network_generations(server_id,generation) VALUES(NEW.server_id,1) ON CONFLICT(server_id) DO UPDATE SET generation=generation+1;
END;

DROP TRIGGER network_generation_supersedes;
CREATE TABLE network_operations_v20 (
 id TEXT PRIMARY KEY CHECK(length(id)=32),
 server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
 kind TEXT NOT NULL CHECK(kind IN ('node_network','egress_create','egress_update','egress_delete','forward_create','forward_update','forward_delete')),
 resource_id INTEGER NOT NULL, resource_revision INTEGER NOT NULL, generation INTEGER NOT NULL,
 request_hash TEXT NOT NULL CHECK(length(request_hash)=64),
 status TEXT NOT NULL CHECK(status IN ('saved','queued','publish_failed','waiting_agent','apply_failed','applied','superseded')),
 desired_revision INTEGER NOT NULL DEFAULT 0, desired_hash TEXT NOT NULL DEFAULT '',
 attempts INTEGER NOT NULL DEFAULT 0, retry_revision INTEGER NOT NULL DEFAULT 0,
 republish INTEGER NOT NULL DEFAULT 0 CHECK(republish IN (0,1)), next_attempt_at TEXT NOT NULL,
 message TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
INSERT INTO network_operations_v20 SELECT * FROM network_operations;
DROP TABLE network_operations;
ALTER TABLE network_operations_v20 RENAME TO network_operations;
CREATE INDEX idx_network_operations_pending ON network_operations(status,next_attempt_at,server_id);
CREATE INDEX idx_network_operations_server ON network_operations(server_id,created_at DESC,id);
CREATE TRIGGER network_generation_supersedes AFTER UPDATE OF generation ON server_network_generations BEGIN
 UPDATE server_network_generations SET attempts=0,retry_revision=0,next_attempt_at='1970-01-01T00:00:00.000000000Z' WHERE server_id=NEW.server_id;
 UPDATE network_operations SET status='superseded',message='已被较新的服务器网络配置替代',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
 WHERE server_id=NEW.server_id AND generation<NEW.generation AND status NOT IN ('saved','applied','superseded');
END;
`
