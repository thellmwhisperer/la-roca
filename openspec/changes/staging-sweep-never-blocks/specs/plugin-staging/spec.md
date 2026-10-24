## ADDED Requirements

### Requirement: A staging folder the sweep cannot remove never fails a command

When the staging sweep cannot inspect or remove a staging folder, it SHALL leave
that folder in place, SHALL record the error for that folder, and SHALL go on with
the other folders. The command that ran the sweep SHALL continue as if the folder
were not there.

#### Scenario: An ordinary command runs next to an unremovable leftover

- **WHEN** an initialized home's plugin root holds a `.roca-corpus-bundle-<x>`
  folder last modified two hours ago, with a `sub` folder of mode 0500 that holds
  a file, and the operator runs
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

### Requirement: Doctor reports the leftovers the sweep left

`roca doctor` SHALL report every `.<plugin>-schema-preflight-*` and
`.<plugin>-bundle-*` folder of a bundled plugin that is more than one hour old,
not held, and still present after doctor's own sweep, with its path, its size in
bytes and the cause it is still there. In read-only mode doctor SHALL remove
nothing and SHALL give read-only mode as the cause.

#### Scenario: Doctor names an unremovable leftover in JSON

- **WHEN** an initialized home's plugin root holds the `.roca-corpus-bundle-<x>`
  folder with the mode 0500 `sub` folder and a 5-byte file inside, and the
  operator runs `roca doctor --json`
- **THEN** the command exits 0 and `plugin_staging` lists one leftover whose path
  is that folder, whose size is 5 bytes, and whose cause contains
  `permission denied`

#### Scenario: Doctor names an unremovable leftover in text

- **WHEN** the home holds that same folder and the operator runs `roca doctor`
- **THEN** the command exits 0 and prints one line with the folder's path and
  `permission denied`

#### Scenario: Read-only doctor reports and keeps a removable leftover

- **WHEN** an initialized home's plugin root holds a `.roca-corpus-bundle-<z>`
  folder last modified two hours ago that the operator could remove, and the
  operator runs `ROCA_READ_ONLY=1 roca doctor --json`
- **THEN** the command exits 0, `plugin_staging` lists that folder with a cause
  that names read-only mode, and the folder still exists

#### Scenario: Doctor says there is nothing left

- **WHEN** an initialized home's plugin root holds no staging folder and the
  operator runs `roca doctor`
- **THEN** the command exits 0 and prints that no plugin staging folder is left
