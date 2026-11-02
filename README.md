# RustWatch

**RustWatch** is a free Windows app that keeps an eye on your Rust server while you play. It connects through the official Rust+ companion system — the same one the phone app uses — and puts everything in one window: your team, a live map, world events with spawn forecasts, shop search, smart devices, a player tracker and team-chat commands. Alerts go to Windows notifications, in-game team chat or Discord.

One `.exe`, no installer. **[Download the latest release](https://github.com/KyranW02/rustwatch/releases/latest)** — after that, RustWatch tells you when an update is out and installs it in one click.

*Not affiliated with Facepunch Studios.*

## First run

1. Double-click `RustWatch.exe`. (Windows SmartScreen may warn because the exe isn't code-signed — More info → Run anyway.)
2. **Servers → Sign in with Steam.** Your default browser (Chrome, Edge, Brave, Opera/Opera GX, Vivaldi or Firefox) opens the official Rust+ login in a separate RustWatch window with its own clean profile. Sign in to Steam there; it closes itself. If the default browser can't be used, RustWatch falls back to another installed one and says which.
3. In Rust: **Esc → Rust+ → Pair with server.** The server appears and connects within seconds.
4. Pair devices in-game: look at a smart switch / alarm / storage monitor, hold **E → Pair**.

Try it without Rust: `RustWatch.exe --demo` runs against a built-in simulated server.

Everything RustWatch writes goes in its data folder, `Documents\RustWatch` by default:

```
Documents\RustWatch\
  config.json              settings, servers, tracked players
  logs\app-DATE.log        what RustWatch did (connections, Steam login, errors)
  logs\alerts-DATE.log     every alert
  logs\chat-DATE.log       team chat
```

Move it from **Settings → Files** (or start with `RustWatch.exe --data "D:\RustWatch"`). Logs older than 30 days are deleted. The login/app-window browser profiles are big caches, so they live in `%LOCALAPPDATA%\RustWatch` to stay out of OneDrive. Nothing is ever written next to the exe.

## What it does

| Rustalytics feature | RustWatch |
|---|---|
| Team UI | Online / offline / AFK / dead, deaths this wipe, kills this wipe + lifetime K/D and hours (free Steam Web API key), online time, location, promote leader. Alerts when teammates come online, go offline, die or go AFK. |
| Smart devices | Toggle switches, name them so `!turrets` works in chat, alarm alerts, storage monitor contents and TC upkeep (time left and the clock time it decays) with low-upkeep warning. Automations switch things when an alarm goes off or a raid is heard; raid mode flips a set of switches from a button or `!raidmode`. Loot log of every storage-monitor box change, with an alert when a box is emptied. |
| Base cameras | CCTV, PTZ cameras, auto turrets and drones through Rust+ (while you're off the server): look around, aim/fire/reload turrets, fly drones, see who's in view. |
| Map events | Cargo (spawn, dock, egress, leave), patrol heli (spawn, down, leave), chinook + locked crate drops, small/large oil rig with crate timer, Bradley with respawn estimate, travelling vendor. Live map like the in-game one: grid, monuments, shops (click for orders), teammates with facing direction, your map pins and death marker, cargo/heli/chinook with heading, crates, explosions, vendor, event areas. |
| Vending machines | Search who sells / buys any item, item-for-sale alerts with max price, track a shop for new/removed orders, new-shop alerts. |
| Player trackers | Tag players with a label and colour, alerts when they join or leave (and when everyone with a tag is offline), 7-day online timeline, an hour-by-day heatmap of when they play, playtime 24h/7d, likely teams from players who join and leave together, live online list with each player's real session length, join/leave feed. Reads the player list straight from the server (free), and follows players by Steam profile on servers that hide names; optional BattleMetrics API key. |
| Sound detection | Since Rust's Aug 2026 update stopped sending events to Rust+, RustWatch listens to Rust's own audio (Windows 11 process loopback; all PC audio on older Windows) and recognises event sounds you teach it once on the Sounds page: cargo, oil rigs called, excavator, Deep Sea, and nearby Bradley, heli crash and vendor. Detections drive the event timers, alerts and chat commands. |
| Raid callouts | Hears raid explosions (C4, rockets, satchels, catapults…) in Rust's audio, estimates where they are from left/right balance, your facing (Rust+) and the game's close/distant sound variants, and posts an estimated grid to team chat when 4+ agree. Ignores gunfire, grenades and music. |
| Server stats | Population every 10 minutes for two weeks, busiest and quietest hours, next forced wipe countdown. Map layers for team trails (last hour) and where the team died this wipe. |
| Map file layers | From the map file Rust caches for each server you join: large rock formations (name god rocks / anvils once per model), caves, train tunnel entrances, monument no-build zones, beach spawns, biomes, estimated cargo route. |
| Raid costs | Explosives and sulfur for every wall, door and deployable, a raid planner with the cheapest mix, `!raidcost`. |
| Spawn forecasts | Next patrol heli / cargo / chinook drop / vendor window ("in 1h 30m – 3h 30m", "could spawn any time · latest ~21:40"), timed from the wipe or a detected server restart before the first sighting, reset on every sighting, and learned from this server's real intervals after a few spawns. Alert when an event is due; `!next` in chat. |
| Chat commands | `!pop !time !wipe !team !cargo !heli !chinook/!ch47/!crate !oil !smoil !loil !bradley !vendor !buy !sell !craft !recycle !grecycle !switch <id> <name> !<switch> [on/off] !switches !promote/!leader !tracked !timer !timers !upkeep !loot !raid !raidmode [off] !raidcost <target> [n] !events !next !help !version !notification` — prefix configurable (`! . , : ;`). |
| Alerts | Per-alert routing to Windows notifications, in-game team chat and a Discord webhook. |
| Discord | Optional Discord bot: run commands from a Discord channel, copy team chat there, and post Discord messages into team chat. |
| Items wiki | Crafting cost, workbench, safe-zone and monument recycler yields; fuzzy search with slang (`ak`, `tc`, `hqm`, `level 3 workbench`). |

Not included: Rustalytics' paid partner-server stat lookups and 24/7 cloud hosting.

## Notes

- The player list comes straight from the Rust server's query port (the same data server browsers use), found automatically from the pairing. If it can't find it, enter the server's connect address (IP:port) under **Players**. Players tracked by name are matched by name. Many servers hide real names from that list (Rust's `server.censorplayerlist`, on by default, swaps them for random stand-ins), so track those players by **Steam profile link** instead: RustWatch asks Steam which server a public profile is playing on, using the same free Steam Web API key as kills. BattleMetrics has required a paid subscription since July 2026; adding a key in Settings switches to it.
- Kills come from Steam's lifetime Rust stats (free key from steamcommunity.com/dev/apikey). They count every server a player plays on; "wipe" kills count from the first reading after the wipe. Players with private game details show no stats.
- Bradley and heli-down detection is inferred from explosion markers; the Bradley respawn time (60 min) and other timings are adjustable in Settings.
- Closing the window quits the app unless **Keep running after closing the window** is on.

## Updates

RustWatch checks this repo's [Releases](https://github.com/KyranW02/rustwatch/releases) every 6 hours (Settings → Updates). When a newer version is published it shows an **Update to x.y.z** button; clicking **Update now** downloads `RustWatch.exe`, checks it against `RustWatch.exe.sha256` from the same release, swaps it in and restarts. Nothing installs without that click.

Publishing a release is automatic: bump `Version` in `internal/app/app.go`, add a `## v<Version>` section to `CHANGELOG.md`, commit and push, then on GitHub open **Actions → Release → Run workflow** (or push a tag `v<Version>`). GitHub Actions ([release.yml](.github/workflows/release.yml)) runs the tests, builds `RustWatch.exe` and `RustWatch.exe.sha256` on GitHub, and publishes the release with that changelog section as its description.

## Building from source

Go 1.22+, standard library only:

```
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-H windowsgui" -o RustWatch.exe ./cmd/rustwatch
go test ./...
```

Item data comes from rustlabs via the open-source rustplusplus project; the Rust+ protocol and FCM pairing follow liamcottle's rustplus.js. Not affiliated with Facepunch Studios or Rustalytics.

### How spawn forecasts work

Vanilla Rust spawns patrol heli, cargo and the chinook crate drop on a schedule that picks a random delay (2–4 h by default) and counts from server start. Population doesn't change it — population only scales loot, ore and animal respawns. Because the delay is random, RustWatch shows a window rather than an exact time. Before the first sighting the window counts from the wipe, or from a server restart RustWatch noticed (the server dropping the connection and coming back after 45 s or more). After two or three sightings it uses the intervals it actually measured on this server, so modded servers and population-based plugins are handled. Adjust the defaults or clear learned timings in Settings → Event spawn timing.
