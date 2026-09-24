# GoSipTea

A terminal SIP softphone for Omarchy, written in Go with Bubble Tea. It uses
baresip for SIP and audio.

This project is under active development. It must not replace the existing
`oma.sip` plugin or its user service yet.

## Intended scope

- Configure one SIP account
- Make, answer, reject and end one call at a time
- Mute and do-not-disturb controls
- Search and edit baresip contacts
- Resolve caller names from local contacts
- Select PipeWire input and output nodes
- Keep the latest 200 call attempts and redial from the call history
- Show desktop notifications and pause all MPRIS media players for incoming calls

The TUI owns the baresip process. Closing the TUI stops baresip. Quitting during
a call requires confirmation.

## Development

```sh
make test
make check
make build
```

Do not stop the existing `baresip.service` for development. The new binary
checks the session bus and refuses to start while another baresip instance is
active. Disabling the old plugin belongs to the later migration step.

Install the binary and desktop entry for the current user with:

```sh
make install
```

After source changes, rebuild and replace the installed binary with:

```sh
make update
```

After that migration, start the TUI from the app launcher as `GoSipTea`
or run:

```sh
gosiptea
```

Caller matching uses country calling code `49` by default. Override it with
`-country-code` when needed.

Real SIP tests use a production account and require an explicit manual step.
Automated tests use temporary directories and simulated baresip events.

## SIP diagnostics

To capture the first registration as well as subsequent calls:

```sh
gosiptea -baresip-log ~/gosiptea-baresip.log -sip-trace
```

`-sip-trace` enables baresip's trace before registration starts and requires
`-baresip-log`. Without it, logging records only normal baresip output unless
tracing is enabled later through D-Bus. Logs contain phone numbers, network
addresses and SIP authentication headers. Do not share them without redaction.

## Structure

- `internal/baresip` owns the child process and D-Bus connection.
- `internal/app` contains the call state machine and caller matching.
- `internal/session` serializes events, commands and persistence changes.
- `internal/storage` reads and writes baresip-compatible files.
- `internal/tui` contains the Bubble Tea interface without platform I/O.

## Reference implementation

The behavior is based on the MIT-licensed `oma.sip` Omarchy plugin by Vinicius
Galleti. Its copyright notice is retained in `LICENSE`.
