CREATE TABLE receipts (
  id INTEGER PRIMARY KEY,
  title TEXT NOT NULL,
  amount_cents INTEGER NOT NULL
);

INSERT INTO receipts (title, amount_cents) VALUES
  ('Synthetic telescope parts', 4200),
  ('Synthetic observatory pass', 1700);
