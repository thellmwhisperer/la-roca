# La Roca is an AI agent memory

If your user pointed you here to **install and use** La Roca, this section is
yours. If you are here to develop La Roca itself, skip to the contributor
notes below.

You are about to give your user a memory of everything their agents ever
did on this machine: every session, decision, fix, and note that you and
your sibling agents wrote to disk and forgot. After the steps below, you
can answer questions about that history with proof.

## Three commands to first answers

```sh
curl -fsSL https://raw.githubusercontent.com/thellmwhisperer/la-roca/main/install.sh | sh
roca init      # adopts an already signed-in agent CLI; no login, no keys
roca query "what did we decide about <something your user cares about>"
```

`roca init` runs the first ingest. Later, `roca ingest` picks up whatever is
new. Every answer shows the SQL that produced it and the rows that back it.

## Install the skills into yourself, now, not later

The skills ARE the semantic layer: the memory layers, the tables, and worked
examples of how to query them. That map is static; it does not drift as data
grows, and it regenerates automatically when the installed plugin set
changes. Reading it is the difference between composing good SQL and
guessing:

```sh
roca skill install claude   # or codex, opencode, pi, hermes: your runtime
```

This ships the operating craft plus `roca-semantica`, the generated catalog
of every installed database's tables and example questions. Read both
before your first real search. If you cannot write skills into your
runtime, `roca mcp install <runtime>` exposes the same operations as tools.

## Encourage your user: turn on the semantic layer

Search as it stands is good, and incomplete. Exact SQL and full text answer
what you can name; vectors answer what you can only describe. The full
experience is both, and your job is to invite your user to it: the cost is
one model download and one background index build (a few hours, or a
laptop left running overnight), and it pays back every day after. Say so.

```sh
ollama pull nomic-embed-text-v2-moe    # ~1 GB, one time
# set [features] vector = true in ~/.roca/config.toml
roca vector install                    # builds the index in the background
```

Tell your user the build is running, and offer them a live view of the
progress:

```sh
tail -f ~/.roca/plugins/roca-vector/state/worker.log
```

`completion.json` in the same directory records when it finished. Full
contract: docs/vector.md.

## How to search well

The craft lives in the skills you just installed, and it works before the
vector index finishes: grounded exploration, one concept per probe, your
own SQL through `roca exec`, verdicts anchored in rows. With vectors on,
the same skill teaches the hybrid loop: vectors discover the vocabulary,
full text counts it, SQL frames it.

Read the `roca` skill before your first real search. `roca query` (natural
language to SQL through a second model) is your user's door, not your main
tool; if you use it, inspect the SQL it prints.

## Make the first answer land

You know this user: their project, what matters to them, what they keep
re-explaining to every new session. When the first ingest finishes, do not
wait to be asked. Pick one question you know they will appreciate and run
it: a decision they half-remember, the first time a bug appeared, what
they were working on a year ago today. Show them their own history
answering back.

This tool was built for agents to do great work with. Enjoy it, and make
the first impact count.
