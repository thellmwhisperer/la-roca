# Language is data, not code

The rule of 2026-08-05: the words the cascade recognizes are data,
never code. No Spanish or English word that the query recognizes may live as a
literal in a `.go` file, because what is hardcoded does not extend — *"this
would not work for an Italian"*. An operator who speaks a language this binary
does not ship adds it through an overlay, without recompiling.

## Where the vocabulary lives

`data/language.yaml` is the embedded pack. It is organized **by function first,
by language second**:

```yaml
project_markers:
  en: [project]
  es: [proyecto]
greetings:
  en: [hi, hello]
  es: [hola, "hola que tal", buenas]
```

The language code is **freeform**. The loader (`internal/query/language.go`)
unions every language a function declares, so a language this binary does not
ship is just another key — nothing in Go changes when `it` or `fr` appears.

The functions the pack feeds: project markers, discovery stems, written
numbers, structural markers, search phrases, openers, stop words, interrogatives
(question words, stripped before the search term is built), low-signal words,
courtesy prefixes, term separators, greetings, write commands, out-of-scope
markers, ambiguous bare words, aggregation words and phrases, and filter markers.

## What is NOT in the pack, on purpose

- **The diacritic folding table** (`internal/query/text.go`). That is
  character-level normalization, not vocabulary, and it already covers the
  letters Italian and Portuguese carry.
- **The agent aliases** (`internal/query/extraction.go`). Those are the public
  names of the runtimes in the source matrix (TECH-SPEC 5.1), not words a human
  language translates: an Italian calls Claude "Claude".
- **The refusal messages** (`internal/query/cascade.go`). Those are
  operator-facing output bound to a contract reason, not input the cascade
  recognizes.

## Extending the vocabulary: the overlay

Drop a `language.yaml` beside the config (the same directory as `config.toml`,
reported by `config.Paths.Language`). Its shape is the same as the embedded
pack, and its entries are **added** to the embedded ones at startup — the
overlay never replaces. An Italian operator writes:

```yaml
project_markers:
  it: [progetto]
write_commands:
  it: ["scrivi "]
greetings:
  it: [ciao]
aggregation_words:
  it: {quante: count}
```

and `il progetto atelier` extracts `atelier`, `scrivi una nota` is refused
out-of-scope, and `ciao` is greeted back with a refusal. No recompile, no
restart beyond the next command: the overlay is read at `service.Open`.

A malformed overlay is an error that names the file, consistent with a malformed
`config.toml`. A missing overlay is the common case and uses the embedded pack.

## The guard

`TestNoLinguisticLiteralLivesInProductionGo` (in `internal/query`) reads the
pack's own vocabulary and asserts none of it appears as a string literal in the
package's production Go. The check runs in `make check`. Single English words
that are also column or table names (`project`, `count`, `sessions`) are out of
the grep's reach on purpose: the guard cannot tell a linguistic literal from a
SQL one, and the structural argument — the collections are populated from the
pack, so there is no `var stopWords = ...` left to regress into — carries those.
