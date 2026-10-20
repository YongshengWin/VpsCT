package store

// NAT settings have their own CAS revision. Ordinary server edits cannot erase
// them, and clearing a setting retains its revision and wire requirement.
const natSchema = `
CREATE TABLE server_nat_configs (
 server_id INTEGER PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
 revision INTEGER NOT NULL CHECK(revision BETWEEN 1 AND 9007199254740991),
 config TEXT CHECK(config IS NULL OR json_valid(config)),
 updated_at TEXT NOT NULL
);
CREATE TRIGGER nat_publication_insert AFTER INSERT ON server_nat_configs BEGIN
 UPDATE server_network_generations SET generation=generation+1 WHERE server_id=NEW.server_id;
END;
CREATE TRIGGER nat_publication_update AFTER UPDATE OF revision,config ON server_nat_configs
 WHEN NEW.revision<>OLD.revision OR NEW.config IS NOT OLD.config BEGIN
 UPDATE server_network_generations SET generation=generation+1 WHERE server_id=NEW.server_id;
END;`
