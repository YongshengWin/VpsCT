package store

const permissionGrantSchema = `
CREATE TABLE permission_grants (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 granted_by INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 permission TEXT NOT NULL CHECK(length(permission) BETWEEN 3 AND 80),
 scope TEXT NOT NULL CHECK(json_valid(scope)),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 CHECK(user_id <> granted_by),
 UNIQUE(user_id,granted_by,permission)
);
CREATE INDEX permission_grants_user ON permission_grants(user_id);
CREATE INDEX permission_grants_grantor ON permission_grants(granted_by);
`
