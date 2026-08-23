# La Roca is an AI agent memory

If your user pointed you here to **install and use** La Roca, this section is
yours. If you are here to develop La Roca itself, skip to the contributor
notes below.

You are about to give your user a memory of everything their agents ever
did on this machine: every session, decision, fix, and note that you and
your sibling agents wrote to disk and forgot. After the steps below, you
can answer questions about that history with proof.

## Step one: install, initialize, and get the first answer

`roca init` creates the database, reads the agent history already on this
machine, writes the skills into the runtimes it finds, and does not return
until word search works. The last thing it does is prove it: it takes a word
out of your user's own history and asks the index for it.

Then, in that same run, it asks one question. There is no second command to
learn: see below.

```sh
curl -fsSL https://raw.githubusercontent.com/thellmwhisperer/la-roca/main/install.sh | sh
roca init      # adopts an already signed-in agent CLI; no login, no keys
roca query "what did we decide about <something your user cares about>"
```

`roca init` runs the first ingest. Later, `roca ingest` picks up whatever is
new. `roca query` is hybrid search with labeled evidence; `roca exec` is the
checked SELECT seat.

## Read the installed skills, now, not later

The skills ARE the semantic layer: the memory layers, the tables, and worked
examples of how to query them. That map is static; it does not drift as data
grows, and it regenerates automatically when the installed plugin set
changes. Reading it is the difference between composing good SQL and
guessing:

This ships the operating craft plus `roca-semantica`, the generated catalog
of every installed database's tables and example questions. Read both
before your first real search. If you cannot write skills into your
runtime, `roca mcp install <runtime>` exposes the same operations as tools.

## Step two (optional): add vector search

Do not make the first working answer depend on vectors. Full-text search works
first. During interactive init, after word search works, La Roca asks once for
permission to find history by meaning and to download the model it needs. A
yes records the decision and starts the background build.

If they decline during init and choose semantic search later, set
`vector = true` under `[features]` in `~/.roca/config.toml` (or in
`config.toml` next to
the database selected with `--db-path`), then start the build:

```sh
roca vector install
```

Progress is a fraction of your user's own history, not a chunk count:

```sh
roca vector status
```

A pass that stopped partway is not an empty product: what it already read
answers, and word search answers for the rest. If this machine cannot embed at
all, init says so and gives one next step; it is never a wall. Full contract:
docs/vector.md.

## How to search well

Read `roca-operations` before your first real search. Write the SQL yourself
against the semantic catalog and run it with `roca exec`. That is the craft.
`roca query` is hybrid search (FTS plus vector, zero answering-model
inference). Anything that spends inference is last resort: `roca playground`
and `roca explore`. `--full` is for humans; agents do not use it.

A fresh install has no vector index. Exec still works. When the index exists,
that same skill makes the hybrid loop mandatory: vector search finds the
nearby rows, full text counts them, SQL frames them through exec.
Zero inference on that path; inference only at the end, to narrate.

The `roca-vector` skill owns the deep pass: starting it, watching it, and
keeping it fresh. Invite the user (one laptop night, daily reward). Do not
wait for the index to search well.

## Make the first answer land

You know this user: their project, what matters to them, what they keep
re-explaining to every new session. When the first ingest finishes, do not
wait to be asked. Pick one question you know they will appreciate and run
it: a decision they half-remember, the first time a bug appeared, what
they were working on a year ago today. Show them their own history
answering back.

This tool was built for agents to do great work with. Enjoy it, and make
the first impact count.

## Maintaining this file

Keep this file for knowledge useful to almost every future agent session in this project.
Do not repeat what the codebase already shows; point to the authoritative file or command instead.
Prefer rewriting or pruning existing entries over appending new ones.
When updating this file, preserve this bar for all agents and keep entries concise.
