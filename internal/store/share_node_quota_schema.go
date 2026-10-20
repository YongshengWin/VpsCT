package store

// The generation barrier prevents a concurrent publication from restoring an
// entry after a quota crossing or settings edit. Counters below the limit do
// not create a new network generation on every heartbeat.
const shareNodeQuotaSchema = `ALTER TABLE shares ADD COLUMN node_usage TEXT NOT NULL DEFAULT '{}';
 CREATE TRIGGER network_generation_share_targets AFTER UPDATE OF targets ON shares
 WHEN NEW.targets IS NOT OLD.targets BEGIN
 UPDATE server_network_generations SET generation=generation+1 WHERE server_id IN (SELECT server_id FROM nodes WHERE share_id=NEW.id AND source='deployed');
 END;
 CREATE TRIGGER network_generation_share_node_usage AFTER UPDATE OF node_usage ON shares
 WHEN NEW.node_usage IS NOT OLD.node_usage BEGIN
 UPDATE server_network_generations SET generation=generation+1 WHERE server_id IN (
 SELECT nodes.server_id FROM nodes, json_each(NEW.targets) AS target, json_each(target.value,'$.quota_bytes') AS quota
 WHERE nodes.share_id=NEW.id AND nodes.source='deployed'
 AND nodes.server_id=json_extract(target.value,'$.server_id') AND nodes.protocol=quota.key AND quota.value>0
 AND ((CASE OLD.billing_mode WHEN 'up' THEN COALESCE(json_extract(OLD.node_usage, '$."' || nodes.id || '"[0]'),0) WHEN 'down' THEN COALESCE(json_extract(OLD.node_usage, '$."' || nodes.id || '"[1]'),0) WHEN 'max' THEN max(COALESCE(json_extract(OLD.node_usage, '$."' || nodes.id || '"[0]'),0),COALESCE(json_extract(OLD.node_usage, '$."' || nodes.id || '"[1]'),0)) ELSE COALESCE(json_extract(OLD.node_usage, '$."' || nodes.id || '"[0]'),0)+COALESCE(json_extract(OLD.node_usage, '$."' || nodes.id || '"[1]'),0) END) >= quota.value) IS NOT ((CASE NEW.billing_mode WHEN 'up' THEN COALESCE(json_extract(NEW.node_usage, '$."' || nodes.id || '"[0]'),0) WHEN 'down' THEN COALESCE(json_extract(NEW.node_usage, '$."' || nodes.id || '"[1]'),0) WHEN 'max' THEN max(COALESCE(json_extract(NEW.node_usage, '$."' || nodes.id || '"[0]'),0),COALESCE(json_extract(NEW.node_usage, '$."' || nodes.id || '"[1]'),0)) ELSE COALESCE(json_extract(NEW.node_usage, '$."' || nodes.id || '"[0]'),0)+COALESCE(json_extract(NEW.node_usage, '$."' || nodes.id || '"[1]'),0) END) >= quota.value)
 );
 END;`
