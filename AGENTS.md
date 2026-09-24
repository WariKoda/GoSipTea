# AGENTS.md

GoSipTea ist ein SIP-Softphone im Terminal für Omarchy, geschrieben in Go mit Bubble Tea. baresip übernimmt SIP und Audio, die TUI besitzt dessen Prozess. Wer die TUI schließt, beendet auch baresip.

## So ist das Projekt aufgebaut

- `cmd/gosiptea` startet alles und verdrahtet Session, TUI und Dispatch. Neue Flags gehören in `main.go` neben die bestehenden.
- `internal/app` enthält die reine Domänenlogik, also Anrufzustände, Registrierung und Kontaktabgleich. Der Code bleibt ohne I/O, damit Tests deterministisch laufen.
- `internal/session` koordiniert genau einen baresip-Prozess und serialisiert alle Zustandsänderungen über Requests. Direkte D-Bus- oder Prozesszugriffe aus anderen Paketen sind falsch.
- `internal/tui` zeigt nur an und sendet Actions. Es liest weder Dateien noch D-Bus noch Uhrzeit außer über den übergebenen Snapshot.
- `internal/baresip` besitzt Prozessstart und D-Bus-Verbindung.
- `internal/storage` liest und schreibt Dateien im `~/.baresip`-Format. Das Format muss kompatibel bleiben.
- `internal/audio`, `internal/media`, `internal/notify`, `internal/desktop`, `internal/bootstrap` kapseln je eine Plattformfunktion: PipeWire-Knoten, MPRIS-Pause, Benachrichtigung, Fensterfokus, Erstkonfiguration.
- `docs/decisions.md` hält fest, was Version eins kann und was nicht. Wer den Umfang ändert, aktualisiert die Datei mit.

Version eins unterstützt ein SIP-Konto und ein Gespräch gleichzeitig, nur auf dem aktuellen Omarchy-System. Globale Tastenkürzel und Aktionen in Benachrichtigungen gehören nicht dazu.

## Befehle

- `make test` startet alle Tests.
- `make check` startet Tests mit Race Detector plus `go vet`. Vor jedem Commit laufen lassen.
- `make build` baut das Binary `./gosiptea`.
- `make install` und `make update` installieren für den aktuellen Benutzer. `update` nach Quelländerungen erneut aufrufen.

Das Modul heißt `github.com/nibra/gosiptea` und braucht Go 1.27.

## Sicherheit im Entwicklungsbetrieb

- Den laufenden `baresip.service` im Entwicklungsbetrieb weder stoppen noch neu starten noch deaktivieren.
- Die App verweigert den Start, solange der D-Bus-Name `com.github.Baresip` einen Besitzer hat. Diese Prüfung nicht umgehen oder aufweichen.
- Automatisierte Tests nutzen niemals das produktive SIP-Konto. Sie arbeiten mit temporären Verzeichnissen und simulierten baresip-Ereignissen. Echte SIP-Tests bleiben ein expliziter manueller Schritt.
- baresip-Logs enthalten Rufnummern, Adressen und SIP-Auth-Daten. Mit `-baresip-log` und `-sip-trace` nur gezielt aufzeichnen und vor dem Teilen schwärzen. `-sip-trace` verlangt `-baresip-log`.
- Neue Dateien unter `~/.baresip` mit restriktiven Rechten anlegen, wie es der bestehende Code tut.

## Konventionen im Code

- Bestehende Muster übernehmen statt neue einzuführen. Vor dem Ändern die Nachbarfunktionen lesen.
- Fehler mit Paketpräfix und `%w` verketten, also etwa `fmt.Errorf("session: load contacts: %w", err)`.
- Kontext mit Timeout an D-Bus-, Prozess- und Audioaufrufe übergeben. Die Timeouts aus `session.Config` und `operationTimeout` in `main.go` wiederverwenden.
- Snapshots bleiben unveränderlich. Slices beim Veröffentlichen kopieren, Passwörter nie in Snapshots oder TUI-Strukturen legen.
- Plattformzugriffe hinter den Interfaces in `session.Dependencies` verstecken und Tests über `NewWithDependencies` mit Fakes schreiben.
- `EnsureConfig` ersetzt keine bestehende Konfiguration. Dieses Verhalten beibehalten.
- Anruferabgleich nutzt Standard-Ländercode `49`. Das Flag `-country-code` bleibt die einzige Stelle für Abweichungen.
- TUI-Texte und Registrierungsdetails über `app.ClampText` und die Längenkonstanten aus `storage` begrenzen.
- Beenden während eines Anrufs fragt nach und legt danach auf. Auch unerwartetes TUI-Ende muss den eigenen baresip-Prozess beenden.
