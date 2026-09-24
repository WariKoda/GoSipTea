# Confirmed decisions

- Version one supports only the current Omarchy system.
- One SIP account and one concurrent call are sufficient.
- All phone functions stop when the TUI exits.
- Quitting during a call asks for confirmation and then hangs up.
- Unexpected TUI termination must also terminate its baresip process.
- An incoming call needs a desktop notification. Global keybindings and
  notification actions are out of scope.
- An incoming call also focuses the window hosting the TUI, so Hyprland
  switches to its workspace. The focus is not returned when the call ends.
- Account, contact and audio formats remain compatible with `~/.baresip`.
- The call history keeps the latest 200 attempts in
  `~/.baresip/gosiptea-call-history.json`. It includes connected, missed,
  rejected, busy, DND and failed outgoing calls. Selecting an entry can dial
  its saved target again.
- Automated tests do not use the production SIP account.

## Safety during development

Development commands must not stop, restart or disable an existing
`baresip.service`. GoSipTea must refuse to start its own baresip process while
the D-Bus name `com.github.Baresip` already has an owner.
