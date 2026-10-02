# embedded-postgres (vendored fork)

A verbatim copy of `github.com/fergusstrange/embedded-postgres` **v1.34.0**
(MIT — see `LICENSE`), pinned through a `replace` directive in
`desktop/go.mod`, with one patch:

`pg_ctl` and `initdb` are started with `hideWindow` (`hide_windows.go` /
`hide_other.go`) so the child processes are created with
`CREATE_NO_WINDOW`. Upstream builds these commands with plain
`exec.Command` and exposes no hook for Windows child-creation flags, so a GUI
process would otherwise flash a console window for every database step.

To update: re-copy the upstream source at the new version, re-apply
`hideWindow(...)` to the two `exec.Command` calls in `embedded_postgres.go`
and the one in `prepare_database.go`, then bump the version in `desktop/go.mod`.
