package store

const personalTemplateSchema = `
CREATE TABLE personal_templates (
 template_id INTEGER PRIMARY KEY REFERENCES rule_templates(id) ON DELETE CASCADE,
 owner_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX personal_templates_owner ON personal_templates(owner_id);
CREATE TABLE share_personal_templates (
 share_id INTEGER NOT NULL REFERENCES shares(id) ON DELETE CASCADE,
 format TEXT NOT NULL CHECK(format IN ('mihomo','surge','shadowrocket','singbox')),
 owner_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 template_id INTEGER NOT NULL REFERENCES personal_templates(template_id) ON DELETE CASCADE,
 PRIMARY KEY(share_id,format)
);
CREATE TRIGGER share_personal_template_transfer AFTER UPDATE OF user_id ON shares
WHEN OLD.user_id IS NOT NEW.user_id BEGIN
 DELETE FROM share_personal_templates WHERE share_id=NEW.id;
END;
CREATE TRIGGER share_personal_template_insert BEFORE INSERT ON share_personal_templates
WHEN NOT EXISTS (
 SELECT 1 FROM shares s JOIN personal_templates p ON p.owner_id=s.user_id
 JOIN rule_templates t ON t.id=p.template_id
 WHERE s.id=NEW.share_id AND s.user_id=NEW.owner_id
 AND p.template_id=NEW.template_id AND t.kind=NEW.format
) BEGIN
 SELECT RAISE(ABORT,'invalid personal template selection');
END;
CREATE TRIGGER share_personal_template_update BEFORE UPDATE ON share_personal_templates
WHEN NOT EXISTS (
 SELECT 1 FROM shares s JOIN personal_templates p ON p.owner_id=s.user_id
 JOIN rule_templates t ON t.id=p.template_id
 WHERE s.id=NEW.share_id AND s.user_id=NEW.owner_id
 AND p.template_id=NEW.template_id AND t.kind=NEW.format
) BEGIN
 SELECT RAISE(ABORT,'invalid personal template selection');
END;
CREATE TRIGGER personal_template_owner_immutable BEFORE UPDATE OF owner_id ON personal_templates
WHEN OLD.owner_id IS NOT NEW.owner_id BEGIN
 SELECT RAISE(ABORT,'personal template owner is immutable');
END;
CREATE TRIGGER personal_template_kind_in_use BEFORE UPDATE OF kind ON rule_templates
WHEN OLD.kind IS NOT NEW.kind AND EXISTS (SELECT 1 FROM share_personal_templates WHERE template_id=OLD.id) BEGIN
 SELECT RAISE(ABORT,'personal template is in use');
END;
CREATE TRIGGER personal_template_delete_owner AFTER DELETE ON personal_templates BEGIN
 DELETE FROM rule_templates WHERE id=OLD.template_id;
END;
`
