# Support your agent in three steps

La Roca ships its agent parsers in the same binary. Supporting another agent is
a normal pull request: one synthetic fixture folder, one parser file, and one
registry line. There is no external parser loading or plugin installation.

> **Privacy rule: never copy real conversation data into a fixture.** Reproduce
> the real structure, field names, ordering, and edge cases with entirely
> invented identities and content. Remove tokens, account IDs, local paths,
> repository names, prompts, answers, and other user data. A fixture that is not
> unmistakably synthetic cannot be accepted.

## 1. Add a synthetic fixture folder

Copy one of the worked examples under
`internal/ingest/parsers/testdata/conformance/` and give the new folder your
parser's stable name. It contains:

- `fixture.json`, which names the parser, its destination, source file,
  scan metadata, and the normalized records expected from it;
- one source file whose extension is arbitrary because encoding belongs to the
  parser, not to the contribution contract.

Use the smallest honest example that contains one complete record. Include the
agent's real structural markers but only fabricated content. The shared suite
automatically discovers every fixture folder and checks that the registered
parser:

- claims its own fixture and rejects a foreign control;
- returns the golden normalized sessions, exchanges, or memories;
- emits no record outside its declared destination;
- has exactly the registry entry named by the fixture.

Run the red test before writing the parser:

```sh
go test ./internal/ingest/parsers -run TestRegisteredParsersConform
```

## 2. Add one parser file

Add one Go file under `internal/ingest/parsers/`. Implement the two-method
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
returns `Records`: corpus-bound `Session` values with their exchanges, or
store-bound `Memory` values. It must not read the database, consult the clock,
contact a service, or invent missing provenance. A source that says nothing
about a value leaves it absent. Report an independently unreadable source record
as a `Discard`; do not lose the rest of a valid file.

## 3. Add one registry line

Add the parser to `registry` in `internal/ingest/parsers/registry.go`. Declare
where its normalized records belong:

```go
Registration{
    Name: "nova", SourceAgent: "nova",
    Locations: []string{".nova/sessions"},
    Destination: DestinationCorpus,
    Parser: novaParser{},
}
```

`Locations` are narrow session-store directories relative to the operator's
home (or absolute locations when the agent has one platform-independent path).
The generic scanner walks only those roots, asks `Detect` about each regular
file after the unchanged-file fingerprint gate, and routes claimed files
through this registration. You do not add a filename extension, decoder, or
second scanner switch anywhere else.

Choose `DestinationCorpus` for raw conversations, `DestinationStore` for
distilled memories, or `DestinationBoth` only when one agent source genuinely
produces both. The registration never declares JSON, JSONL, Markdown, or any
other encoding. Destination validation runs before normalized records reach a
writer.

Run the parser suite, then the repository gate:

```sh
go test ./internal/ingest/parsers
make check
```

Open a pull request containing the fixture, parser, and registry line together.
The existing fixture directories are the canonical worked examples; keep a new
contribution as small and synthetic as they are.
