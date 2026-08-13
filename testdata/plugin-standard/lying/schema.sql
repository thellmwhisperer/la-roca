CREATE TABLE invoices (
  id INTEGER PRIMARY KEY,
  label TEXT NOT NULL,
  paid_cents INTEGER NOT NULL
);

INSERT INTO invoices (label, paid_cents) VALUES ('Synthetic lunar invoice', 900);
