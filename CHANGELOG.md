# Changelog

Each version's section becomes the description of its GitHub release.

## v1.1.3
- **Player tracker:** recognises servers that hide real player names (Rust's `server.censorplayerlist`) and says so, instead of showing random stand-in names as if they were real.
- **Player tracker:** shows when a server only sends part of its player list (e.g. "85 of 211").
- Includes everything from 1.1.1 and 1.1.2 below.

## v1.1.2
- **Player tracker:** retries when part of a big player list gets lost, handles both packet formats servers use, and explains clearly when a server won't share its player list (player and queue counts still show).
- **Shops:** shows what Rust+ is sending (markers, shops, last error) and explains when the server reports no shops.
- **Diagnostics:** Settings → Files → Save diagnostics writes a troubleshooting file (no passwords or keys).

## v1.1.1
- Shop diagnostics (included in 1.1.2).

## v1.1.0
- First public release: team tracking, live map, world events with spawn forecasts, shop search, smart devices, player tracker, team-chat commands, Steam kill stats, Discord alerts and in-app updates.
