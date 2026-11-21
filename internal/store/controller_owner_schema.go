package store

const controllerOwnerSchema = `
CREATE TABLE controller_owner (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 user_id INTEGER NOT NULL UNIQUE REFERENCES users(id) ON DELETE RESTRICT
);
INSERT INTO controller_owner(singleton,user_id)
 SELECT 1,id FROM users WHERE role='admin' ORDER BY enabled DESC,id LIMIT 1;
`
