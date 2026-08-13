CREATE TABLE archived_messages (
  id INTEGER PRIMARY KEY,
  body TEXT NOT NULL
);

INSERT INTO archived_messages (body) VALUES ('Synthetic beacon acknowledged');
