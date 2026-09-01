# Support your agent in four steps

La Roca ships its agent parsers in the same binary. Supporting another agent is
a normal pull request: one real-store measurement, one synthetic fixture folder,
one parser file, and one registry line. There is no external parser loading or
plugin installation.

> **Privacy rule: never copy real conversation data into a fixture.** Reproduce
> the real structure, field names, ordering, and edge cases with entirely
> invented identities and content. Remove tokens, account IDs, local paths,
> repository names, prompts, answers, and other user data. A fixture that is not
> unmistakably synthetic cannot be accepted.

## 0. Measure the real store before writing a fixture

This step is mandatory. On a machine where the agent has real sessions, inspect
its store read-only before inventing any test data. Record all of the following:

- the store and session path layout, including the primary content filename;
- total file count and total bytes, plus counts and sizes for candidate content
  files;
- record-type counts inside the primary candidates;
- which secondary surfaces were checked and whether they add unique records or
  only repeat primary data.

Put those aggregate measurements in the pull request under a **Real-store
measurement** heading. Never quote conversation text, identifiers, account data
or literal local paths. A fixture that cannot be traced from its field names,
nesting and record types back to this measured shape is invalid, even when the
synthetic conformance test passes. If no populated real store is available, the
parser is not ready to contribute: do not substitute documentation, guessed
JSON or an author-invented fixture for measurement.

## 1. Add a synthetic fixture folder

Copy one of the worked examples under
`pkg/parsers/testdata/conformance/` and give the new folder your
parser's stable name. It contains:

- `fixture.json`, which names the parser, its destination, source file,
  scan metadata, and the normalized records expected from it;
- one source file whose extension is arbitrary because encoding belongs to the
  parser, not to the contribution contract.

Use the smallest honest example that contains one complete record. Include the
agent's real structural markers but only fabricated content. The shared suite
automatically discovers every fixture folder and checks that the registered
parser:

- claims its own fixture, and rejects both a foreign control and every fixture
  another registered parser owns;
- returns the golden normalized sessions, exchanges, tool calls (including
  session-level orphan calls), or memories;
- emits no record outside its declared destination;
- labels every normalized record with the registry's canonical harness;
- has exactly the registry entry named by the fixture.

Run the red test before writing the parser:

```sh
go test ./pkg/parsers -run TestRegisteredParsersConform
```

## 2. Add one parser file

Add one Go file under `pkg/parsers/`. Implement the two-method
`Parser` interface:

```go
type Parser interface {
    Detect(File) bool
    Parse(File) (Records, error)
}
```

`Detect` answers only whether the candidate belongs to this agent. Prefer
stable markers recorded by the source itself; a filename or `.json`, `.jsonl`,
or `.md` suffix is not proof of ownership. The parser can use any encoding
internally, but format must never become its public identity.

`Parse` is deterministic normalization. It receives bytes plus `FileMeta` and
returns `Records`: corpus-bound `Session` values with their exchanges and any
tool calls outside completed exchanges in `OrphanedTools`, or store-bound
`Memory` values. It must not read the database, consult the clock,
contact a service, or invent missing provenance. A source that says nothing
about a value leaves it absent. Report an independently unreadable source record
as a `Discard`; do not lose the rest of a valid file.

## 3. Add one registry line

Add the parser to `registry` in `pkg/parsers/registry.go`. Declare
where its normalized records belong:

```go
Registration{
    Name: "nova", SourceAgent: "nova",
    CanonicalHarness: "Nova CLI",
    Locations: []string{".nova/sessions"},
    Version: "nova-v1",
    Destination: DestinationCorpus,
    Parser: novaParser{},
}
```

`Name` is the stable identifier the ingest fingerprint and the fixture manifest
share. `SourceAgent` is the agent this source belongs to: it names the line in
the ingest summary and rides in the scan metadata your parser receives, and it
falls back to `Name` when left empty.

`CanonicalHarness` is the product surface that opened the artifact, such as
`Claude Code`, `Codex CLI`, `OpenCode`, or `Grok Build`. The registry knows this
deterministically; never search for it in JSON or copy an incidental agent name
from metadata. The conformance harness refuses an empty declaration and checks
that the value lands on every normalized session or memory. A source-recorded
model remains separate and stays empty when the artifact states none.

`Locations` are narrow session-store directories relative to the operator's
home (or absolute locations when the agent has one platform-independent path).
The generic scanner walks only those roots, asks `Detect` about each regular
file after the unchanged-file fingerprint gate, and routes claimed files
through this registration. You do not add a filename extension, decoder, or
second scanner switch anywhere else. An empty location, the home directory
itself, and anything climbing out of it are refused: the harness fails the
registry line and a shipped one warns the operator instead of walking the
machine.

`FileName`, when set, narrows a directory `Location` to the one store file that
source owns, so a database directory that also holds WAL, SHM and manifest
sidecars is scanned without admitting a foreign file the directory holds. A
`Location` that is itself a file is always read literally, so one line can
declare both a narrow directory and exact files; leave `FileName` empty to keep
a directory walking every regular file beneath it.

The same locations name what `TestRegisteredParsersHarvestPresentAgentStores`
reads. That smoke is opt-in twice over: it runs only when you set
`ROCA_REAL_HARVEST=1`, and then only for the declared stores that exist on the
machine. It never runs inside `make check`, because the stores it walks are the
operator's private conversation history and nobody else's work should be judged
on what is in them. When you do ask for it, it walks the store read-only, runs
`Detect` and `Parse`, and reports store bytes, detected files, sessions,
exchanges, memories, thinking, tools and discards. A large store with a
near-zero conversation or memory yield fails. Established source-specific
scanners declare `HarvestLocations` for this smoke without entering the generic
scan route.

`Version` is the reading your parser currently gives that source. It rides
inside the ingest watermark, so when a later change teaches `Detect` or `Parse`
to read more, bump it. Files already synced under the poorer reading are then
read again by a plain `roca ingest` instead of staying skipped forever behind
the fingerprint they earned. Leave it empty only while the reading has never
changed.

Choose `DestinationCorpus` for raw conversations, `DestinationStore` for
distilled memories, or `DestinationBoth` only when one agent source genuinely
produces both. The registration never declares JSON, JSONL, Markdown, or any
other encoding. Destination validation runs before normalized records reach a
writer.

Run the parser suite, then the repository gate:

```sh
ROCA_REAL_HARVEST=1 go test -v ./pkg/parsers \
  -run TestRegisteredParsersHarvestPresentAgentStores
go test ./pkg/parsers
make check
```

Open a pull request containing the real-store aggregate measurements, fixture,
parser, and registry line together. Include the smoke's yield summary. The
existing fixture directories are the canonical worked examples; keep a new
contribution as small and synthetic as they are.
