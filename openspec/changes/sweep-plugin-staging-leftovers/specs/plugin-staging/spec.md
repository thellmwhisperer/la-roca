## ADDED Requirements

### Requirement: Leftover staging folders are removed by the next install

When `roca _install-bundled-plugins` prepares a bundled plugin, it SHALL first remove
every `.<plugin>-schema-preflight-*` and `.<plugin>-bundle-*` folder in the plugin
root that no live process holds, together with everything inside it.

#### Scenario: A killed update's folders are gone after the next install

- **WHEN** the plugin root holds a `.roca-corpus-schema-preflight-<x>` folder with a
  `roca-corpus.db` file inside and two `.roca-corpus-bundle-<y>` folders, none held by
  any process, and the operator runs `roca _install-bundled-plugins --json`
- **THEN** the command exits 0 and none of those three folders exists afterwards

#### Scenario: A second install finds nothing to remove

- **WHEN** `roca _install-bundled-plugins --json` runs twice in a row on a home with no
  other `roca` process running
- **THEN** after each run the plugin root holds no `.*-schema-preflight-*` and no
  `.*-bundle-*` folder

### Requirement: A staging folder in use is never removed

A staging folder SHALL count as in use while a process holds the exclusive advisory
lock on the `.held` file inside it. The creator of a staging folder SHALL take that
lock right after creating the folder and hold it until the folder is removed. The
sweep SHALL test the lock without waiting and SHALL keep the folder when the lock is
held by another process.

#### Scenario: A folder held by another process survives the install

- **WHEN** another process holds the lock on `.held` inside a
  `.roca-corpus-bundle-<z>` folder and the operator runs
  `roca _install-bundled-plugins --json`
- **THEN** the command exits 0 without waiting for that lock, and the
  `.roca-corpus-bundle-<z>` folder and its `.held` file still exist

#### Scenario: Plugin data is never a candidate

- **WHEN** the plugin root holds leftover staging folders next to installed plugin
  folders with their databases and WAL, SHM and journal files, and the operator runs
  `roca _install-bundled-plugins --json`
- **THEN** only the staging folders are removed and every installed plugin folder and
  file is still there
