# plugin-staging Specification

## Purpose
The bundled installer stages each plugin in temporary folders under the plugin
root. This capability says how a folder in use is told apart from a leftover of an
interrupted run, that leftovers are removed without touching plugin data, that a
leftover the sweep cannot remove never fails a command, and how `roca doctor`
reports the leftovers that remain.

## Requirements

### Requirement: Leftover staging folders are removed by the next install

When `roca _install-bundled-plugins` prepares a bundled plugin, it SHALL first remove
every `.<plugin>-schema-preflight-*` and `.<plugin>-bundle-*` folder in the plugin
root that is a leftover, together with everything inside it. A folder is a leftover
when its age is more than one hour and no process holds the lock on the `.held` file
inside it (a folder with no `.held` file counts as not held).

#### Scenario: A killed update's folders are gone after the next install

- **WHEN** the plugin root holds a `.roca-corpus-schema-preflight-<x>` folder with a
  `roca-corpus.db` file inside and two `.roca-corpus-bundle-<y>` folders, all last
  modified two hours ago and none held by any process, and the operator runs
  `roca _install-bundled-plugins --json`
- **THEN** the command exits 0 and none of those three folders exists afterwards

#### Scenario: A second install finds nothing to remove

- **WHEN** the plugin root holds no staging folder before the first run and no other
  `roca` process is running, and `roca _install-bundled-plugins --json` runs twice in a
  row
- **THEN** after each run the plugin root holds no `.*-schema-preflight-*` and no
  `.*-bundle-*` folder

### Requirement: A staging folder in use is never removed

A staging folder SHALL count as in use while its age is one hour or less, or while a
process holds the exclusive advisory lock on the `.held` file inside it. The creator of a staging folder SHALL
take that lock right after creating the folder, SHALL remove the folder before
releasing the lock, and SHALL never leave a folder it is using unlocked for an hour.
The sweep SHALL test the lock without waiting and SHALL keep every folder in use.

#### Scenario: A folder held by another process survives the install

- **WHEN** another process holds the lock on `.held` inside a
  `.roca-corpus-bundle-<z>` folder last modified two hours ago, and the operator runs
  `roca _install-bundled-plugins --json`
- **THEN** the command exits 0 without waiting for that lock, and the
  `.roca-corpus-bundle-<z>` folder and its `.held` file still exist

#### Scenario: A folder just created by another process survives the install

- **WHEN** the plugin root holds a `.roca-corpus-bundle-<w>` folder created a moment
  ago, with no `.held` file yet, and the operator runs
  `roca _install-bundled-plugins --json`
- **THEN** the command exits 0 and the `.roca-corpus-bundle-<w>` folder still exists

#### Scenario: Plugin data is never a candidate

- **WHEN** the plugin root holds leftover staging folders next to installed plugin
  folders with their databases and WAL, SHM and journal files, and the operator runs
  `roca _install-bundled-plugins --json`
- **THEN** only the staging folders are removed and every installed plugin folder and
  file is still there

### Requirement: A staging folder's age is its newest modification time

A staging folder's age SHALL be measured from the newest modification time across
the folder and everything inside it. Content written inside a staging folder within
the last hour SHALL keep that folder fresh, even when the folder's own modification
time is older.

#### Scenario: An old folder with content written a moment ago survives the install

- **WHEN** an initialized home's plugin root holds a `.roca-corpus-bundle-<v>` folder
  whose own modification time is two hours old, with no `.held` file, holding a file
  written a moment ago, and the operator runs `roca _install-bundled-plugins --json`
- **THEN** the command exits 0 and the `.roca-corpus-bundle-<v>` folder still exists
- **AND** a following `roca doctor --json` exits 0 and its `plugin_staging` list is
  empty

### Requirement: A staging folder the sweep cannot remove never fails a command

When the staging sweep cannot inspect or remove a staging folder, it SHALL leave
that folder in place, SHALL record the error for that folder, and SHALL go on with
the other folders. The command that ran the sweep SHALL continue as if the folder
were not there. After a failed removal the sweep SHALL try to restore the
modification times of the folders it walked, so the folder keeps its age, and SHALL
count again the bytes that are still there.

#### Scenario: An ordinary command runs next to an unremovable leftover

- **WHEN** an initialized home's plugin root holds a `.roca-corpus-bundle-<x>`
  folder last modified two hours ago, with a `sub` folder of mode 0500 that holds
  a 5-byte file, and the operator runs
  `roca exec "SELECT count(*) FROM plugin_roca_ops.memories"`
- **THEN** the command exits 0 and prints the count

#### Scenario: The install runs next to an unremovable leftover

- **WHEN** the plugin root holds that same `.roca-corpus-bundle-<x>` folder and the
  operator runs `roca _install-bundled-plugins --json`
- **THEN** the command exits 0 and the `.roca-corpus-bundle-<x>` folder still exists

#### Scenario: A lock file that cannot be locked does not fail the install

- **WHEN** the plugin root holds a `.roca-ops-bundle-<y>` folder whose `.held` is a
  directory, and the operator runs `roca _install-bundled-plugins --json`
- **THEN** the command exits 0

#### Scenario: A partly removed leftover keeps its age and its surviving size

- **WHEN** the plugin root holds that same `.roca-corpus-bundle-<x>` folder with an
  unlocked `.held` file next to the mode 0500 `sub` folder, and the operator runs
  `roca _install-bundled-plugins --json` and then `roca doctor --json` twice
- **THEN** every command exits 0, and both doctor runs list that folder in
  `plugin_staging` with `bytes` 5 and a `cause` that contains `permission denied`

### Requirement: Doctor reports the leftovers the sweep left

`roca doctor` SHALL report every `.<plugin>-schema-preflight-*` and
`.<plugin>-bundle-*` folder of a bundled plugin that is more than one hour old,
not held, and still present after doctor's own sweep, with its path, its size in
bytes and the cause it is still there. In JSON the list SHALL be `plugin_staging`,
one entry per folder with `path`, `bytes` and `cause`. In read-only mode doctor
SHALL remove nothing and SHALL give `read-only mode` as the cause.

#### Scenario: Doctor names an unremovable leftover in JSON

- **WHEN** an initialized home's plugin root holds the `.roca-corpus-bundle-<x>`
  folder with the mode 0500 `sub` folder and a 5-byte file inside, and the
  operator runs `roca doctor --json`
- **THEN** the command exits 0 and `plugin_staging` lists one leftover whose `path`
  is that folder, whose `bytes` is 5, and whose `cause` contains
  `permission denied`

#### Scenario: Read-only doctor reports and keeps a removable leftover

- **WHEN** an initialized home's plugin root holds a `.roca-corpus-bundle-<z>`
  folder last modified two hours ago, holding a 5-byte file, that the operator
  could remove, and the operator runs `ROCA_READ_ONLY=1 roca doctor --json`
- **THEN** the command exits 0, `plugin_staging` lists that folder with `bytes` 5
  and the `cause` `read-only mode`, and the folder still exists

### Requirement: Doctor text gives one line per leftover

In text, `roca doctor` SHALL print one line per reported folder,
`plugin staging: <path> · <bytes> bytes · <cause>`, or the single line
`plugin staging: none leftover` when it reports none.

#### Scenario: Doctor names an unremovable leftover in text

- **WHEN** an initialized home's plugin root holds the `.roca-corpus-bundle-<x>`
  folder with the mode 0500 `sub` folder, and the operator runs `roca doctor`
- **THEN** the command exits 0 and prints one `plugin staging:` line with the
  folder's path and `permission denied`

#### Scenario: Doctor says there is nothing left

- **WHEN** an initialized home's plugin root holds no staging folder and the
  operator runs `roca doctor`
- **THEN** the command exits 0 and prints `plugin staging: none leftover`
