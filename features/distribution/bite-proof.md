# Distribution acceptance bite proof

Run on 2026-08-10 against the real `bin/roca`; every mutation was temporary and reverted immediately after the named red result.

| Feature | Temporary mutation | Focused command | Red result |
|---|---|---|---|
| `cli.feature` | Removed the help pointer added to unknown-command errors in `internal/distribution/cli/cli.go`. | `go test -tags acceptance ./test/acceptance -run 'TestDistributionAcceptance/An_unknown_command' -count=1` | `An unknown command fails with a pointer to help, never silently` failed because the error contained no `help`. |
| `skill.feature` | Disabled the explicit runtime-or-`--all` guard in `internal/distribution/cli/skill.go`. | `go test -tags acceptance ./test/acceptance -run 'TestDistributionAcceptance/Nothing_is_ever_installed' -count=1` | `Nothing is ever installed into an agent without being asked` failed because bare install succeeded. |
| `lifecycle.feature` | Reintroduced the unversioned `ARTEFACT="$BINARY-$PLATFORM"` assignment in `install.sh`. | `go test -tags acceptance ./test/acceptance -run 'TestDistributionAcceptance/The_install_script' -count=1` | `The install script and the Go code agree on every artefact name` failed because the script declared a name absent from the Go release contract. |

After the three reversions, the complete distribution acceptance suite returned green.
