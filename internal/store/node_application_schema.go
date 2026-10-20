package store

const nodeApplicationSchema = `
 INSERT INTO server_network_generations(server_id,generation) SELECT id,1 FROM servers WHERE true ON CONFLICT DO NOTHING;
 CREATE TRIGGER network_generation_server_insert AFTER INSERT ON servers BEGIN
 INSERT INTO server_network_generations(server_id,generation) VALUES(NEW.id,1);
 END;
 CREATE TRIGGER publication_generation_retry AFTER UPDATE OF generation ON server_network_generations
 WHEN NEW.generation<>OLD.generation BEGIN
 UPDATE server_network_generations SET attempts=0,next_attempt_at='',retry_revision=retry_revision+1 WHERE server_id=NEW.server_id;
 END;
 CREATE TRIGGER network_generation_node_projection AFTER UPDATE OF core,params,port,server ON nodes
 WHEN (NEW.source='deployed' OR OLD.source='deployed') AND
 (NEW.core IS NOT OLD.core OR NEW.params IS NOT OLD.params OR NEW.port IS NOT OLD.port OR
 (NEW.server IS NOT OLD.server AND EXISTS(SELECT 1 FROM node_networks WHERE node_id=NEW.id AND json_extract(policy,'$.advertise_mode')='override'))) BEGIN
 UPDATE server_network_generations SET generation=generation+1 WHERE server_id IN (OLD.server_id,NEW.server_id);
 END;
 CREATE TRIGGER network_generation_share_access AFTER UPDATE OF expires_at,quota_bytes ON shares
 WHEN NEW.expires_at IS NOT OLD.expires_at OR NEW.quota_bytes IS NOT OLD.quota_bytes BEGIN
 UPDATE server_network_generations SET generation=generation+1 WHERE server_id IN (SELECT server_id FROM nodes WHERE share_id=NEW.id AND source='deployed');
 END;
 CREATE TABLE node_application_candidates (
 server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
 revision INTEGER NOT NULL, node_id INTEGER NOT NULL,
 payload TEXT NOT NULL CHECK(substr(payload,1,7)='enc:v1:'),
 PRIMARY KEY(server_id,revision,node_id),
 FOREIGN KEY(server_id,revision) REFERENCES desired_states(server_id,revision) ON DELETE CASCADE
 );
 CREATE TABLE node_applied_states (
 server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
 node_id INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
 revision INTEGER NOT NULL, hash TEXT NOT NULL CHECK(length(hash)=64),
	sequence INTEGER NOT NULL DEFAULT 0,
 status TEXT NOT NULL CHECK(status IN ('applied','blocked','failed')),
 payload TEXT NOT NULL CHECK(substr(payload,1,7)='enc:v1:'),
 updated_at TEXT NOT NULL,
 PRIMARY KEY(server_id,node_id)
 );
 CREATE TABLE node_chain_origins (
 chain_id INTEGER PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
 landing_id INTEGER REFERENCES nodes(id) ON DELETE SET NULL,
 resolved INTEGER NOT NULL DEFAULT 0 CHECK(resolved IN (0,1))
 );
 INSERT INTO node_chain_origins(chain_id,landing_id) SELECT id,NULL FROM nodes WHERE source='chain';
 CREATE TRIGGER node_chain_origin_insert AFTER INSERT ON nodes WHEN NEW.source='chain' BEGIN
 INSERT INTO node_chain_origins(chain_id,landing_id) VALUES(NEW.id,NULL);
 END;
 CREATE INDEX idx_maintenance_agent_revision ON maintenance_jobs(server_id,agent_sha COLLATE NOCASE,created_at);`
