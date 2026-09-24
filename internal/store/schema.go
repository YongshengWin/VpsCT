package store

// migrations are applied in order; each entry is one schema version.
var migrations = []string{
	// v1: core schema
	`
CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL);

CREATE TABLE users (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  username TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  role TEXT NOT NULL DEFAULT 'user',
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE sessions (
  id TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  user_agent TEXT NOT NULL DEFAULT '',
  ip TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_sessions_user ON sessions(user_id);

CREATE TABLE servers (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE,
  region TEXT NOT NULL DEFAULT '',
  public_host TEXT NOT NULL DEFAULT '',
  tags TEXT NOT NULL DEFAULT '[]',
  notes TEXT NOT NULL DEFAULT '',
  quota_bytes INTEGER NOT NULL DEFAULT 0,
  quota_reset_day INTEGER NOT NULL DEFAULT 0,
  quota_billing TEXT NOT NULL DEFAULT 'dual',
  core_mode TEXT NOT NULL DEFAULT 'stable',
  ipv4_only INTEGER NOT NULL DEFAULT 0,
  cert_mode TEXT NOT NULL DEFAULT 'self_signed',
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE agents (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  server_id INTEGER NOT NULL UNIQUE REFERENCES servers(id) ON DELETE CASCADE,
  token_hash TEXT NOT NULL DEFAULT '',
  enroll_token_hash TEXT NOT NULL DEFAULT '',
  enroll_expires_at TEXT,
  version TEXT NOT NULL DEFAULT '',
  last_seen_at TEXT,
  applied_revision INTEGER NOT NULL DEFAULT 0,
  applied_hash TEXT NOT NULL DEFAULT '',
  apply_error TEXT NOT NULL DEFAULT '',
  public_ipv4 TEXT NOT NULL DEFAULT '',
  public_ipv6 TEXT NOT NULL DEFAULT '',
  metrics TEXT NOT NULL DEFAULT '{}',
  diagnostics TEXT NOT NULL DEFAULT '{}',
  connlog_seq INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE external_subscriptions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  url TEXT NOT NULL,
  user_agent TEXT NOT NULL DEFAULT '',
  sync_interval_min INTEGER NOT NULL DEFAULT 360,
  last_sync_at TEXT,
  last_error TEXT NOT NULL DEFAULT '',
  upload INTEGER NOT NULL DEFAULT 0,
  download INTEGER NOT NULL DEFAULT 0,
  total INTEGER NOT NULL DEFAULT 0,
  expire_at TEXT,
  node_count INTEGER NOT NULL DEFAULT 0,
  raw_content TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1,
  owner_user_id INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE shares (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
  targets TEXT NOT NULL DEFAULT '[]',
  extra_node_ids TEXT NOT NULL DEFAULT '[]',
  quota_bytes INTEGER NOT NULL DEFAULT 0,
  billing_mode TEXT NOT NULL DEFAULT 'dual',
  reset_day INTEGER NOT NULL DEFAULT 1,
  expires_at TEXT,
  status TEXT NOT NULL DEFAULT 'active',
  template_id INTEGER,
  connlog_enabled INTEGER NOT NULL DEFAULT 0,
  notes TEXT NOT NULL DEFAULT '',
  subscription_id INTEGER,
  period_start TEXT NOT NULL,
  used_upload INTEGER NOT NULL DEFAULT 0,
  used_download INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE nodes (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  protocol TEXT NOT NULL,
  server TEXT NOT NULL DEFAULT '',
  port INTEGER NOT NULL DEFAULT 0,
  params TEXT NOT NULL DEFAULT '{}',
  server_params TEXT NOT NULL DEFAULT '{}',
  source TEXT NOT NULL DEFAULT 'manual',
  server_id INTEGER REFERENCES servers(id) ON DELETE SET NULL,
  listen_port INTEGER NOT NULL DEFAULT 0,
  core TEXT NOT NULL DEFAULT '',
  share_id INTEGER REFERENCES shares(id) ON DELETE SET NULL,
  external_sub_id INTEGER REFERENCES external_subscriptions(id) ON DELETE CASCADE,
  chain_front_node_id INTEGER,
  enabled INTEGER NOT NULL DEFAULT 1,
  owner_user_id INTEGER NOT NULL DEFAULT 0,
  tags TEXT NOT NULL DEFAULT '[]',
  sort_order INTEGER NOT NULL DEFAULT 0,
  revoked INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX idx_nodes_server ON nodes(server_id);
CREATE INDEX idx_nodes_external ON nodes(external_sub_id);
CREATE INDEX idx_nodes_share ON nodes(share_id);

CREATE TABLE rule_templates (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'mihomo',
  description TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL DEFAULT '',
  variables TEXT NOT NULL DEFAULT '{}',
  is_builtin INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE proxy_group_presets (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  groups_json TEXT NOT NULL DEFAULT '[]',
  rules_json TEXT NOT NULL DEFAULT '[]',
  is_builtin INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE subscriptions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  kind TEXT NOT NULL,
  token TEXT NOT NULL DEFAULT '',
  token_hash TEXT NOT NULL UNIQUE,
  token_hint TEXT NOT NULL DEFAULT '',
  short_code TEXT NOT NULL DEFAULT '',
  template_id INTEGER REFERENCES rule_templates(id) ON DELETE SET NULL,
  default_format TEXT NOT NULL DEFAULT 'mihomo',
  proxy_groups TEXT NOT NULL DEFAULT '[]',
  chains TEXT NOT NULL DEFAULT '[]',
  rules TEXT NOT NULL DEFAULT '[]',
  rule_providers TEXT NOT NULL DEFAULT '{}',
  node_selection TEXT NOT NULL DEFAULT '{}',
  uploaded_content TEXT NOT NULL DEFAULT '',
  source_external_id INTEGER REFERENCES external_subscriptions(id) ON DELETE SET NULL,
  expire_at TEXT,
  traffic_limit_bytes INTEGER NOT NULL DEFAULT 0,
  userinfo_header INTEGER NOT NULL DEFAULT 1,
  show_info_nodes INTEGER NOT NULL DEFAULT 0,
  owner_user_id INTEGER NOT NULL DEFAULT 0,
  allowed_user_ids TEXT NOT NULL DEFAULT '[]',
  share_id INTEGER REFERENCES shares(id) ON DELETE CASCADE,
  enabled INTEGER NOT NULL DEFAULT 1,
  access_count INTEGER NOT NULL DEFAULT 0,
  last_access_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX idx_subscriptions_short ON subscriptions(short_code) WHERE short_code <> '';

CREATE TABLE traffic_samples (
  server_id INTEGER NOT NULL,
  node_id INTEGER,
  ts TEXT NOT NULL,
  rx_bytes INTEGER NOT NULL,
  tx_bytes INTEGER NOT NULL
);
CREATE INDEX idx_samples_ts ON traffic_samples(ts);
CREATE INDEX idx_samples_subject ON traffic_samples(server_id, node_id, ts);

CREATE TABLE counter_state (
  server_id INTEGER NOT NULL,
  counter_key TEXT NOT NULL,
  epoch TEXT NOT NULL DEFAULT '',
  last_rx INTEGER NOT NULL DEFAULT 0,
  last_tx INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (server_id, counter_key)
);

CREATE TABLE traffic_hourly (
  bucket TEXT NOT NULL,
  subject TEXT NOT NULL,
  subject_id INTEGER NOT NULL,
  up INTEGER NOT NULL DEFAULT 0,
  down INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (bucket, subject, subject_id)
);

CREATE TABLE traffic_daily (
  bucket TEXT NOT NULL,
  subject TEXT NOT NULL,
  subject_id INTEGER NOT NULL,
  up INTEGER NOT NULL DEFAULT 0,
  down INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (bucket, subject, subject_id)
);

CREATE TABLE desired_states (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  revision INTEGER NOT NULL,
  payload TEXT NOT NULL,
  hash TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  applied_at TEXT,
  UNIQUE (server_id, revision)
);

CREATE TABLE audit_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ts TEXT NOT NULL,
  user_id INTEGER,
  username TEXT NOT NULL DEFAULT '',
  action TEXT NOT NULL,
  target TEXT NOT NULL DEFAULT '',
  detail TEXT NOT NULL DEFAULT '{}',
  ip TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_audit_ts ON audit_log(ts);

CREATE TABLE access_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  subscription_id INTEGER NOT NULL,
  ts TEXT NOT NULL,
  ip TEXT NOT NULL DEFAULT '',
  user_agent TEXT NOT NULL DEFAULT '',
  format TEXT NOT NULL DEFAULT '',
  status INTEGER NOT NULL DEFAULT 200
);
CREATE INDEX idx_access_sub ON access_log(subscription_id, ts);
CREATE INDEX idx_access_ts ON access_log(ts);

CREATE TABLE ban_rules (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  kind TEXT NOT NULL,
  value TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1,
  expires_at TEXT,
  created_at TEXT NOT NULL
);

CREATE TABLE settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE share_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  share_id INTEGER NOT NULL REFERENCES shares(id) ON DELETE CASCADE,
  ts TEXT NOT NULL,
  kind TEXT NOT NULL,
  detail TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_share_events ON share_events(share_id, ts);
`,
	// v2: user avatar, TOTP two-factor auth, last login
	`
ALTER TABLE users ADD COLUMN avatar TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN totp_secret TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN totp_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN totp_last_step INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN recovery_codes TEXT NOT NULL DEFAULT '[]';
ALTER TABLE users ADD COLUMN last_login_at TEXT;
`,
	// v3: display nickname (login still uses username)
	`
ALTER TABLE users ADD COLUMN nickname TEXT NOT NULL DEFAULT '';
`,
	// v4: monthly traffic reset for generated/imported subscriptions
	`
ALTER TABLE subscriptions ADD COLUMN reset_day INTEGER NOT NULL DEFAULT 0;
`,
	// v5: default traffic accounting is two-way (inbound+outbound)
	`
UPDATE servers SET quota_billing='dual' WHERE quota_billing='sum';
UPDATE shares SET billing_mode='dual' WHERE billing_mode='sum';
`,
	// v6: durable, scoped agent maintenance commands and results.
	`
CREATE TABLE maintenance_jobs (
  id TEXT PRIMARY KEY,
  server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  request TEXT NOT NULL,
  status TEXT NOT NULL,
  result TEXT NOT NULL,
  report_token TEXT NOT NULL,
  report_hash TEXT NOT NULL,
  agent_sha TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX idx_maintenance_active ON maintenance_jobs(server_id) WHERE status IN ('queued','running');
`,
	// v7: credential-free accounting identities outlive node deletion so final
	// agent reports still reach the original node and share ledgers.
	`
CREATE TABLE node_meter_identities (
 node_id INTEGER PRIMARY KEY,
 server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
 listen_port INTEGER NOT NULL,
 core TEXT NOT NULL,
 share_id INTEGER REFERENCES shares(id) ON DELETE SET NULL
);
INSERT INTO node_meter_identities SELECT id,server_id,listen_port,core,share_id FROM nodes WHERE server_id IS NOT NULL;
CREATE TRIGGER node_meter_insert AFTER INSERT ON nodes WHEN NEW.server_id IS NOT NULL BEGIN
 INSERT OR REPLACE INTO node_meter_identities VALUES(NEW.id,NEW.server_id,NEW.listen_port,NEW.core,NEW.share_id);
END;
CREATE TRIGGER node_meter_update AFTER UPDATE ON nodes WHEN NEW.server_id IS NOT NULL BEGIN
 INSERT OR REPLACE INTO node_meter_identities VALUES(NEW.id,NEW.server_id,NEW.listen_port,NEW.core,NEW.share_id);
END;
`,
	`ALTER TABLE subscriptions ADD COLUMN short_code_hash TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_sub_short_hash ON subscriptions(short_code_hash);`,
	`ALTER TABLE users ADD COLUMN security_version INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN security_version INTEGER NOT NULL DEFAULT 0;`,
	// Final snapshot acknowledgments commit with usage, never before it.
	`CREATE TABLE meter_settlements (
 server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
 batch_id TEXT NOT NULL,
 created_at TEXT NOT NULL,
 PRIMARY KEY(server_id,batch_id)
 );`,
	// Completed uninstall receipts must survive server deletion for callback retries.
	`CREATE TABLE maintenance_jobs_new (
 id TEXT PRIMARY KEY,
 server_id INTEGER NOT NULL,
 request TEXT NOT NULL,
 status TEXT NOT NULL,
 result TEXT NOT NULL,
 report_token TEXT NOT NULL,
 report_hash TEXT NOT NULL,
 agent_sha TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 delete_server INTEGER NOT NULL DEFAULT 0
 );
 INSERT INTO maintenance_jobs_new SELECT *,0 FROM maintenance_jobs;
 DROP TABLE maintenance_jobs;
 ALTER TABLE maintenance_jobs_new RENAME TO maintenance_jobs;
 CREATE UNIQUE INDEX idx_maintenance_active ON maintenance_jobs(server_id) WHERE status IN ('queued','running');`,
	// Observational inventory is independent of server and share quotas.
	`CREATE TABLE network_snapshots (
 server_id INTEGER PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
 source TEXT NOT NULL, snapshot TEXT NOT NULL, received_at TEXT NOT NULL
 );
 CREATE TABLE network_sources (
 server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
 source TEXT NOT NULL, sequence INTEGER NOT NULL,
 PRIMARY KEY(server_id,source)
 );
 CREATE TABLE network_interfaces (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
 interface_id TEXT NOT NULL, snapshot TEXT NOT NULL,
 present INTEGER NOT NULL, last_seen_at TEXT NOT NULL,
 epoch TEXT NOT NULL, valid INTEGER NOT NULL, rx INTEGER NOT NULL, tx INTEGER NOT NULL,
 UNIQUE(server_id,interface_id)
 );
 CREATE TRIGGER network_interface_delete AFTER DELETE ON network_interfaces BEGIN
 DELETE FROM traffic_hourly WHERE subject='interface' AND subject_id=OLD.id;
 DELETE FROM traffic_daily WHERE subject='interface' AND subject_id=OLD.id;
 END;`,
	`CREATE TABLE network_billing_policies (
 server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
 revision INTEGER NOT NULL, policy TEXT NOT NULL, created_at TEXT NOT NULL,
 PRIMARY KEY(server_id,revision)
 );
 CREATE TABLE network_billing_state (
 server_id INTEGER PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
 current_revision INTEGER NOT NULL DEFAULT 0, requested_revision INTEGER NOT NULL DEFAULT 0,
 rx_total INTEGER NOT NULL DEFAULT 0, tx_total INTEGER NOT NULL DEFAULT 0,
 snapshot_source TEXT NOT NULL DEFAULT '', snapshot_sequence INTEGER NOT NULL DEFAULT 0,
 status TEXT NOT NULL DEFAULT 'legacy', error TEXT NOT NULL DEFAULT '',
 applied_at TEXT, sampled_at TEXT
 );
 CREATE TABLE network_billing_baselines (
 server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
 interface_id TEXT NOT NULL, source TEXT NOT NULL, generation TEXT NOT NULL,
 sequence INTEGER NOT NULL, rx INTEGER NOT NULL, tx INTEGER NOT NULL,
 PRIMARY KEY(server_id,interface_id)
 );
 CREATE TABLE network_billing_sources (
 server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
 collector_id TEXT NOT NULL, sequence INTEGER NOT NULL,
 PRIMARY KEY(server_id,collector_id)
 );
 CREATE TABLE network_billing_settlements (
 server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
 batch_id TEXT NOT NULL, payload_hash TEXT NOT NULL, created_at TEXT NOT NULL,
 PRIMARY KEY(server_id,batch_id)
 );`,
	`CREATE TABLE egress_profiles (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
 name TEXT NOT NULL, kind TEXT NOT NULL,
 enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
 current_revision INTEGER NOT NULL CHECK(current_revision BETWEEN 1 AND 4096),
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 UNIQUE(id,server_id),
 FOREIGN KEY(id,current_revision) REFERENCES egress_profile_revisions(profile_id,revision) DEFERRABLE INITIALLY DEFERRED
 );
 CREATE INDEX idx_egress_server ON egress_profiles(server_id,id);
 CREATE TABLE egress_profile_revisions (
 profile_id INTEGER NOT NULL,
 server_id INTEGER NOT NULL,
 revision INTEGER NOT NULL CHECK(revision BETWEEN 1 AND 4096),
 config TEXT NOT NULL CHECK(json_valid(config)),
 created_at TEXT NOT NULL,
 PRIMARY KEY(profile_id,revision),
 UNIQUE(profile_id,revision,server_id),
 FOREIGN KEY(profile_id,server_id) REFERENCES egress_profiles(id,server_id) ON DELETE CASCADE
 );
 CREATE TRIGGER egress_revision_immutable BEFORE UPDATE ON egress_profile_revisions BEGIN
 SELECT RAISE(ABORT,'egress revision is immutable');
 END;
 CREATE UNIQUE INDEX idx_nodes_network_owner ON nodes(id,server_id);
 CREATE TABLE node_networks (
 node_id INTEGER PRIMARY KEY,
 server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
 revision INTEGER NOT NULL CHECK(revision BETWEEN 1 AND 9007199254740991),
 policy TEXT NOT NULL CHECK(json_valid(policy)),
 egress_profile_id INTEGER,
 egress_revision INTEGER,
 updated_at TEXT NOT NULL,
 CHECK((egress_profile_id IS NULL)=(egress_revision IS NULL)),
 CHECK(COALESCE(json_extract(policy,'$.egress_profile_id'),0)=COALESCE(egress_profile_id,0)),
 CHECK(COALESCE(json_extract(policy,'$.egress_revision'),0)=COALESCE(egress_revision,0)),
 FOREIGN KEY(node_id,server_id) REFERENCES nodes(id,server_id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
 FOREIGN KEY(egress_profile_id,egress_revision,server_id) REFERENCES egress_profile_revisions(profile_id,revision,server_id) DEFERRABLE INITIALLY DEFERRED
 );
 CREATE INDEX idx_node_network_profile ON node_networks(egress_profile_id,egress_revision);
 CREATE TRIGGER node_network_source BEFORE INSERT ON node_networks WHEN
 (SELECT source FROM nodes WHERE id=NEW.node_id)<>'deployed' BEGIN
 SELECT RAISE(ABORT,'only deployed nodes can bind a network');
 END;
 CREATE TRIGGER node_network_source_update BEFORE UPDATE OF source ON nodes WHEN
 NEW.source<>'deployed' AND EXISTS(SELECT 1 FROM node_networks WHERE node_id=NEW.id AND policy<>'null') BEGIN
 SELECT RAISE(ABORT,'bound node must remain deployed');
 END;`,
	// A server that used bindings cannot safely be managed by an old agent,
	// including after the last node is removed (local fences still need cleanup).
	`CREATE TABLE server_network_requirements (
 server_id INTEGER PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
 binding_version INTEGER NOT NULL CHECK(binding_version=1)
 );
 INSERT INTO server_network_requirements(server_id,binding_version)
 SELECT DISTINCT server_id,1 FROM node_networks WHERE policy<>'null';
 CREATE TRIGGER node_network_requirement_insert AFTER INSERT ON node_networks WHEN NEW.policy<>'null' BEGIN
 INSERT INTO server_network_requirements(server_id,binding_version) VALUES(NEW.server_id,1) ON CONFLICT(server_id) DO NOTHING;
 END;
 CREATE TRIGGER node_network_requirement_update AFTER UPDATE OF policy ON node_networks WHEN NEW.policy<>'null' BEGIN
 INSERT INTO server_network_requirements(server_id,binding_version) VALUES(NEW.server_id,1) ON CONFLICT(server_id) DO NOTHING;
 END;`,
	// v16: durable single-server network publication and exact apply receipts.
	`CREATE TABLE server_network_generations (
 server_id INTEGER PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
 generation INTEGER NOT NULL CHECK(generation BETWEEN 1 AND 9007199254740991),
 published_generation INTEGER NOT NULL DEFAULT 0,
 attempts INTEGER NOT NULL DEFAULT 0,
 retry_revision INTEGER NOT NULL DEFAULT 0,
 next_attempt_at TEXT NOT NULL DEFAULT '1970-01-01T00:00:00.000000000Z'
 );
 INSERT INTO server_network_generations(server_id,generation) SELECT server_id,1 FROM server_network_requirements;
 CREATE TABLE network_operations (
 id TEXT PRIMARY KEY CHECK(length(id)=32),
 server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
 kind TEXT NOT NULL CHECK(kind='node_network'), resource_id INTEGER NOT NULL,
 resource_revision INTEGER NOT NULL, generation INTEGER NOT NULL,
 request_hash TEXT NOT NULL CHECK(length(request_hash)=64),
 status TEXT NOT NULL CHECK(status IN ('queued','publish_failed','waiting_agent','apply_failed','applied','superseded')),
 desired_revision INTEGER NOT NULL DEFAULT 0, desired_hash TEXT NOT NULL DEFAULT '',
 attempts INTEGER NOT NULL DEFAULT 0, retry_revision INTEGER NOT NULL DEFAULT 0,
 republish INTEGER NOT NULL DEFAULT 0 CHECK(republish IN (0,1)), next_attempt_at TEXT NOT NULL,
 message TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL
 );
 CREATE INDEX idx_network_operations_pending ON network_operations(status,next_attempt_at,server_id);
 CREATE INDEX idx_network_operations_server ON network_operations(server_id,created_at DESC,id);
 CREATE TRIGGER network_generation_supersedes AFTER UPDATE OF generation ON server_network_generations BEGIN
 UPDATE server_network_generations SET attempts=0,retry_revision=0,next_attempt_at='1970-01-01T00:00:00.000000000Z' WHERE server_id=NEW.server_id;
 UPDATE network_operations SET status='superseded',message='已被较新的服务器网络配置替代',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
 WHERE server_id=NEW.server_id AND generation<NEW.generation AND status NOT IN ('applied','superseded');
 END;
 CREATE TRIGGER network_generation_insert AFTER INSERT ON node_networks BEGIN
 INSERT INTO server_network_requirements(server_id,binding_version) VALUES(NEW.server_id,1) ON CONFLICT(server_id) DO NOTHING;
 INSERT INTO server_network_generations(server_id,generation) VALUES(NEW.server_id,1) ON CONFLICT(server_id) DO UPDATE SET generation=generation+1;
 END;
 CREATE TRIGGER network_generation_update AFTER UPDATE ON node_networks BEGIN
 INSERT INTO server_network_requirements(server_id,binding_version) VALUES(NEW.server_id,1) ON CONFLICT(server_id) DO NOTHING;
 INSERT INTO server_network_generations(server_id,generation) VALUES(NEW.server_id,1) ON CONFLICT(server_id) DO UPDATE SET generation=generation+1;
 END;
 CREATE TRIGGER network_generation_delete AFTER DELETE ON node_networks BEGIN
 INSERT INTO server_network_generations(server_id,generation) SELECT OLD.server_id,1 WHERE EXISTS(SELECT 1 FROM servers WHERE id=OLD.server_id)
 ON CONFLICT(server_id) DO UPDATE SET generation=generation+1;
 END;
 CREATE TRIGGER network_generation_profile AFTER UPDATE OF enabled ON egress_profiles WHEN OLD.enabled<>NEW.enabled
 AND EXISTS(SELECT 1 FROM node_networks WHERE egress_profile_id=NEW.id) BEGIN
 INSERT INTO server_network_generations(server_id,generation) VALUES(NEW.server_id,1) ON CONFLICT(server_id) DO UPDATE SET generation=generation+1;
 END;
 -- Once opted in, legacy mutations of desired-state inputs share the same
 -- save/build barrier and outbox. Observational heartbeats and usage increments
 -- deliberately do not invalidate a candidate or restart configured services.
 CREATE TRIGGER network_generation_node_insert AFTER INSERT ON nodes WHEN NEW.source='deployed' BEGIN
 UPDATE server_network_generations SET generation=generation+1 WHERE server_id=NEW.server_id;
 END;
 CREATE TRIGGER network_generation_node_delete AFTER DELETE ON nodes WHEN OLD.source='deployed' BEGIN
 UPDATE server_network_generations SET generation=generation+1 WHERE server_id=OLD.server_id;
 END;
 CREATE TRIGGER network_generation_node_update AFTER UPDATE OF name,protocol,server_params,source,server_id,listen_port,share_id,enabled,sort_order,revoked ON nodes
 WHEN (NEW.source='deployed' OR OLD.source='deployed') AND
 (NEW.name IS NOT OLD.name OR NEW.protocol IS NOT OLD.protocol OR NEW.server_params IS NOT OLD.server_params OR NEW.source IS NOT OLD.source OR NEW.server_id IS NOT OLD.server_id OR NEW.listen_port IS NOT OLD.listen_port OR NEW.share_id IS NOT OLD.share_id OR NEW.enabled IS NOT OLD.enabled OR NEW.sort_order IS NOT OLD.sort_order OR NEW.revoked IS NOT OLD.revoked) BEGIN
 UPDATE server_network_generations SET generation=generation+1 WHERE server_id IN (OLD.server_id,NEW.server_id);
 END;
 CREATE TRIGGER network_generation_server_update AFTER UPDATE OF name,public_host,core_mode,ipv4_only,cert_mode,enabled ON servers
 WHEN NEW.name IS NOT OLD.name OR NEW.public_host IS NOT OLD.public_host OR NEW.core_mode IS NOT OLD.core_mode OR NEW.ipv4_only IS NOT OLD.ipv4_only OR NEW.cert_mode IS NOT OLD.cert_mode OR NEW.enabled IS NOT OLD.enabled BEGIN
 UPDATE server_network_generations SET generation=generation+1 WHERE server_id=NEW.id;
 END;
 CREATE TRIGGER network_generation_share_update AFTER UPDATE OF status,connlog_enabled ON shares
 WHEN NEW.status IS NOT OLD.status OR NEW.connlog_enabled IS NOT OLD.connlog_enabled BEGIN
 UPDATE server_network_generations SET generation=generation+1 WHERE server_id IN (SELECT server_id FROM nodes WHERE share_id=NEW.id AND source='deployed');
 END;
 CREATE TRIGGER network_generation_setting_insert AFTER INSERT ON settings
 WHEN NEW.key IN ('core.singbox_version','core.snell_version','core.singbox_sha256','core.snell_sha256','connlog.self_enabled') BEGIN
 UPDATE server_network_generations SET generation=generation+1;
 END;
 CREATE TRIGGER network_generation_setting_update AFTER UPDATE ON settings
 WHEN (NEW.key IN ('core.singbox_version','core.snell_version','core.singbox_sha256','core.snell_sha256','connlog.self_enabled') OR OLD.key IN ('core.singbox_version','core.snell_version','core.singbox_sha256','core.snell_sha256','connlog.self_enabled')) AND (NEW.key IS NOT OLD.key OR NEW.value IS NOT OLD.value) BEGIN
 UPDATE server_network_generations SET generation=generation+1;
 END;
 CREATE TRIGGER network_generation_setting_delete AFTER DELETE ON settings
 WHEN OLD.key IN ('core.singbox_version','core.snell_version','core.singbox_sha256','core.snell_sha256','connlog.self_enabled') BEGIN
 UPDATE server_network_generations SET generation=generation+1;
 END;`,
	// v17: profile edits share request receipts; metadata-only edits are saved,
	// never falsely reported as applied by an agent.
	`DROP TRIGGER network_generation_supersedes;
 CREATE TABLE network_operations_v17 (
 id TEXT PRIMARY KEY CHECK(length(id)=32),
 server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
 kind TEXT NOT NULL CHECK(kind IN ('node_network','egress_create','egress_update','egress_delete')), resource_id INTEGER NOT NULL,
 resource_revision INTEGER NOT NULL, generation INTEGER NOT NULL,
 request_hash TEXT NOT NULL CHECK(length(request_hash)=64),
 status TEXT NOT NULL CHECK(status IN ('saved','queued','publish_failed','waiting_agent','apply_failed','applied','superseded')),
 desired_revision INTEGER NOT NULL DEFAULT 0, desired_hash TEXT NOT NULL DEFAULT '',
 attempts INTEGER NOT NULL DEFAULT 0, retry_revision INTEGER NOT NULL DEFAULT 0,
 republish INTEGER NOT NULL DEFAULT 0 CHECK(republish IN (0,1)), next_attempt_at TEXT NOT NULL,
 message TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL
 );
 INSERT INTO network_operations_v17 SELECT * FROM network_operations;
 DROP TABLE network_operations;
 ALTER TABLE network_operations_v17 RENAME TO network_operations;
 CREATE INDEX idx_network_operations_pending ON network_operations(status,next_attempt_at,server_id);
 CREATE INDEX idx_network_operations_server ON network_operations(server_id,created_at DESC,id);
 CREATE TRIGGER network_generation_supersedes AFTER UPDATE OF generation ON server_network_generations BEGIN
 UPDATE server_network_generations SET attempts=0,retry_revision=0,next_attempt_at='1970-01-01T00:00:00.000000000Z' WHERE server_id=NEW.server_id;
 UPDATE network_operations SET status='superseded',message='已被较新的服务器网络配置替代',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
 WHERE server_id=NEW.server_id AND generation<NEW.generation AND status NOT IN ('saved','applied','superseded');
 END;`,
	// v18: upstream credentials follow immutable profile revisions. AAD binds
	// the encrypted value to its server/profile/revision, not just its column.
	`CREATE TABLE egress_profile_credentials (
 profile_id INTEGER NOT NULL, server_id INTEGER NOT NULL, revision INTEGER NOT NULL,
 credentials TEXT NOT NULL CHECK(substr(credentials,1,7)='enc:v1:'),
 PRIMARY KEY(profile_id,revision),
 FOREIGN KEY(profile_id,revision,server_id) REFERENCES egress_profile_revisions(profile_id,revision,server_id) ON DELETE CASCADE
 );
 CREATE TRIGGER egress_credentials_immutable BEFORE UPDATE ON egress_profile_credentials BEGIN
 SELECT RAISE(ABORT,'egress credentials are immutable');
 END;`,
	// v19: direct binding support alone is not enough to interpret or clean up
	// an upstream transport. Require explicit support even after final detach.
	`ALTER TABLE server_network_requirements ADD COLUMN egress_version INTEGER NOT NULL DEFAULT 0 CHECK(egress_version IN (0,1));
 UPDATE server_network_requirements SET egress_version=1 WHERE server_id IN (
 SELECT nn.server_id FROM node_networks nn JOIN egress_profiles ep ON ep.id=nn.egress_profile_id WHERE ep.kind<>'direct'
 );
 CREATE TRIGGER node_egress_requirement_insert AFTER INSERT ON node_networks
 WHEN EXISTS(SELECT 1 FROM egress_profiles WHERE id=NEW.egress_profile_id AND kind<>'direct') BEGIN
 INSERT INTO server_network_requirements(server_id,binding_version,egress_version) VALUES(NEW.server_id,1,1)
 ON CONFLICT(server_id) DO UPDATE SET egress_version=1;
 END;
 CREATE TRIGGER node_egress_requirement_update AFTER UPDATE OF egress_profile_id ON node_networks
 WHEN EXISTS(SELECT 1 FROM egress_profiles WHERE id=NEW.egress_profile_id AND kind<>'direct') BEGIN
 INSERT INTO server_network_requirements(server_id,binding_version,egress_version) VALUES(NEW.server_id,1,1)
 ON CONFLICT(server_id) DO UPDATE SET egress_version=1;
 END;`,
	// v20: independent fixed-forward resources and shared listener reservations.
	forwardSchema,
	// v21: immutable acknowledgements survive resource deletion and ACK loss.
	`CREATE TABLE forward_receipts (
 server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
 id TEXT NOT NULL CHECK(length(id)=32), digest TEXT NOT NULL CHECK(length(digest)=64),
 desired_revision INTEGER NOT NULL, created_at TEXT NOT NULL,
 PRIMARY KEY(server_id,id)
 );`,
	// v22: SSH support is independent from SOCKS and remains required on cleanup.
	`ALTER TABLE server_network_requirements ADD COLUMN ssh_version INTEGER NOT NULL DEFAULT 0 CHECK(ssh_version IN (0,1));
 CREATE TRIGGER node_ssh_requirement_insert AFTER INSERT ON node_networks
 WHEN EXISTS(SELECT 1 FROM egress_profiles WHERE id=NEW.egress_profile_id AND kind='ssh') BEGIN
 INSERT INTO server_network_requirements(server_id,binding_version,egress_version,ssh_version) VALUES(NEW.server_id,1,1,1)
 ON CONFLICT(server_id) DO UPDATE SET ssh_version=1,egress_version=1;
 END;
 CREATE TRIGGER node_ssh_requirement_update AFTER UPDATE OF egress_profile_id ON node_networks
 WHEN EXISTS(SELECT 1 FROM egress_profiles WHERE id=NEW.egress_profile_id AND kind='ssh') BEGIN
 INSERT INTO server_network_requirements(server_id,binding_version,egress_version,ssh_version) VALUES(NEW.server_id,1,1,1)
 ON CONFLICT(server_id) DO UPDATE SET ssh_version=1,egress_version=1;
 END;`,
	// v23: independent WireGuard capability and immutable consumer key ownership.
	wireguardSchema,
	// v24: standalone mita cleanup remains required after the final node deletion.
	`CREATE TABLE server_mita_requirements(server_id INTEGER PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,version INTEGER NOT NULL CHECK(version=1));
 CREATE TRIGGER node_mita_insert AFTER INSERT ON nodes WHEN NEW.core='mieru' AND NEW.server_id IS NOT NULL BEGIN
 INSERT INTO server_mita_requirements VALUES(NEW.server_id,1) ON CONFLICT DO NOTHING; END;
 CREATE TRIGGER node_mita_update AFTER UPDATE OF core ON nodes WHEN NEW.core='mieru' AND NEW.server_id IS NOT NULL BEGIN
 INSERT INTO server_mita_requirements VALUES(NEW.server_id,1) ON CONFLICT DO NOTHING; END;`,
	// v25: archive only the display; retain stable identities and accounting.
	`ALTER TABLE network_interfaces ADD COLUMN archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0,1));
 CREATE INDEX idx_interface_history ON network_interfaces(server_id,archived,id);`,
	// v26: WireGuard access endpoints require compatible cleanup after deletion.
	`CREATE TRIGGER node_wg_access_insert AFTER INSERT ON nodes WHEN NEW.protocol='wireguard' AND NEW.source='deployed' BEGIN
 INSERT INTO server_network_requirements(server_id,binding_version,egress_version,wireguard_version) VALUES(NEW.server_id,1,1,1)
 ON CONFLICT(server_id) DO UPDATE SET wireguard_version=1,egress_version=1; END;`,
	// v27: durable two-ended WireGuard lifecycle and hidden landing resources.
	transitSchema,
	// v28: managed SS-2022 uses the same two-ended ledger without a WireGuard requirement.
	`DROP TRIGGER transit_wg_insert;
 CREATE TRIGGER transit_node_insert AFTER INSERT ON nodes WHEN NEW.source='transit' BEGIN
 INSERT INTO server_network_requirements(server_id,binding_version,egress_version,wireguard_version)
 VALUES(NEW.server_id,1,1,CASE WHEN NEW.protocol='wireguard' THEN 1 ELSE 0 END)
 ON CONFLICT(server_id) DO UPDATE SET egress_version=1,wireguard_version=max(wireguard_version,excluded.wireguard_version);
 INSERT INTO server_network_generations(server_id,generation) VALUES(NEW.server_id,1)
 ON CONFLICT(server_id) DO UPDATE SET generation=generation+1;
 END;`,
	// v29: retired transit cards can be removed from the active UI while their
	// accounting identity and audit ledger remain intact.
	`ALTER TABLE managed_transits ADD COLUMN hidden INTEGER NOT NULL DEFAULT 0 CHECK(hidden IN (0,1));`,
	// v30: freeze the effective version before changing fresh-install defaults.
	coreVersionPinMigration,
}
