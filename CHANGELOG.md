# Changelog

Each version's section becomes the description of its GitHub release.

## v1.2.0
- **Hears events in game.** Rust no longer sends cargo, heli, oil rigs or the vendor to Rust+, so RustWatch now listens to Rust's own audio and recognises event sounds: cargo ship, Small and Large Oil Rig being called, the giant excavator, Deep Sea, and nearby Bradley explosions, heli crashes and the travelling vendor. Teach each sound once on the new **Sounds** page: capture the last 30 seconds of game audio (or **import a WAV file** of the sound), drag across the picture to mark the sound, check it and save. After that each sound it hears feeds the event timers (oil rig crate, Bradley respawn, cargo and vendor windows), sends an alert and answers `!cargo`, `!oil`, `!bradley`, `!vendor`, `!excavator` and `!deepsea`. On Windows 11 it records only Rust, so music, Discord and videos can't trigger it. Older Windows records all PC audio. Audio is kept in memory only, apart from the clips you save.
- **Seen in game list with real names.** While Rust is running and you're on the server, the Players page lists the people Steam says you recently played with: real Steam names and profiles from your own Steam client, the same list as Shift+Tab → Players. That works even on servers that hide names publicly. Press **Track** on anyone there to follow them by Steam profile. People you already track by name are linked to their Steam profile automatically when they show up. RustWatch only connects to Steam while Rust is running and never touches the game itself.
- **Track players by Steam profile.** Paste someone's Steam profile link into *Track player*, or press **Link Steam** on someone you already track. RustWatch checks Steam every minute for which server they're on, so it works on servers that hide names (Rust's `server.censorplayerlist`, on by default), on servers that won't share their player list, and after a name change. Needs the free Steam Web API key (Settings → Kills) and a public Steam profile.
- Each Steam-linked player shows what Steam says they're doing: on this server, in Rust on another server, offline, or a private profile.
- **Shops and live events:** Rust's 6 August 2026 update stopped servers sending vending machines, cargo, patrol heli and the travelling vendor to Rust+, so no Rust+ app can show them any more. RustWatch now says so on the Shops page, map and Overview, and in `!buy`, `!sell`, `!cargo`, `!heli` and `!vendor`, instead of looking broken. Spawn windows are still estimated from the wipe and server restarts.
- Fixed the hidden-names notice: BattleMetrics *can* show real names when the server owner has given it admin access, but that feed isn't public.

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
