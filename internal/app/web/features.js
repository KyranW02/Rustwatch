"use strict";
/* Devices extras, enemy patterns, server stats, cameras and raid costs. Uses the helpers in app.js. */

const fmtWhen = (unix) => new Date(unix * 1000).toLocaleString([], { weekday: "short", hour: "2-digit", minute: "2-digit" });
const kshort = (n) => (n >= 1000 ? (n / 1000).toFixed(n >= 10000 ? 0 : 1).replace(/\.0$/, "") + "k" : String(n));

/* ---------- devices: automations, raid mode, loot log ---------- */
window.raidMode = (on) => act("/api/raidmode", { on }, on ? "Raid mode started" : "Raid mode ended").catch(() => {});
window.delRule = (id) => act("/api/automations/delete", { id });
let autoSig = "";
window.renderDeviceExtras = function () {
  const devs = S.devices || [];
  const sw = devs.filter((d) => d.type === 1), al = devs.filter((d) => d.type === 2);
  const rules = S.automations || [];
  $("#autoCount").textContent = rules.length ? `${rules.length} rule${rules.length === 1 ? "" : "s"}` : "";
  $("#autoList").innerHTML = rules.map((r) => `<div class="rule"><i class="trig ${r.trigger === "manual" ? "manual" : r.trigger === "raid" ? "raid" : ""}"></i><span>${esc(r.text)}</span><button class="ghost small" onclick="delRule('${esc(r.id)}')">Remove</button></div>`).join("")
    || `<div class="empty-note">${sw.length ? "No rules yet. Add one below, for example: when the base alarm goes off, turn the turrets on." : "Pair a smart switch first: rules turn switches on or off."}</div>`;
  $("#raidModeBar").hidden = !rules.some((r) => r.trigger === "manual");
  // keep the form's choices in step with the paired devices, without resetting what's picked
  const sig = JSON.stringify([sw.map((d) => [d.entityId, d.name]), al.map((d) => [d.entityId, d.name])]);
  if (sig !== autoSig) {
    autoSig = sig;
    const f = $("#autoForm");
    f.trigger.innerHTML = [...al.map((d) => `<option value="alarm:${d.entityId}">Alarm “${esc(d.name)}” goes off</option>`),
      `<option value="alarm:any">Any alarm goes off</option>`, `<option value="raid">A raid is heard</option>`, `<option value="manual">Raid mode is started</option>`].join("");
    f.switch.innerHTML = sw.map((d) => `<option value="${d.entityId}">${esc(d.name)}</option>`).join("") || `<option value="">No switches paired</option>`;
  }
  const loot = S.loot || [];
  $("#lootLog").innerHTML = loot.map((e) => `<div class="loot ${e.emptied ? "emptied" : ""}"><time>${clock(e.time)}</time><div><b>${esc(e.box)}</b>${e.emptied ? ' <span class="pill bad">Emptied</span>' : ""} <span class="muted small">${ago(e.time)}</span></div>
    <div class="deltas">${e.changes.slice(0, 8).map((c) => `<span class="${c.delta > 0 ? "up" : "down"}">${icon(c.short, c.name)}${c.delta > 0 ? "+" : "−"}${kshort(Math.abs(c.delta))} ${esc(c.name)}${c.bp ? " BP" : ""}</span>`).join("")}${e.changes.length > 8 ? `<span class="muted">+${e.changes.length - 8} more</span>` : ""}</div></div>`).join("")
    || `<div class="empty-note pad">Changes in boxes with a storage monitor show up here: what went in, what came out, and an alert if a box is suddenly emptied.</div>`;
};
$("#autoForm").addEventListener("submit", (e) => {
  e.preventDefault();
  const f = e.target;
  act("/api/automations/save", { trigger: f.trigger.value, switchId: parseInt(f.switch.value) || 0, on: f.on.value === "1", revertMin: parseInt(f.revert.value) || 0 }, "Rule added").then(() => { f.revert.value = 0; }).catch(() => {});
});

/* ---------- players: when they play, likely teams ---------- */
let heatPick = "";
window.renderPlayerExtras = function () {
  const T = S.tracker || {}, tr = T.tracked || [];
  const tags = {};
  tr.forEach((p) => { if (p.tag) (tags[p.tag.toLowerCase()] ||= { label: p.tag, ps: [] }).ps.push(p); });
  const opts = [...Object.values(tags).filter((t) => t.ps.length > 1).map((t) => [`tag:${t.label.toLowerCase()}`, `Everyone tagged ${t.label} (${t.ps.length})`]), ...tr.map((p) => [`p:${p.steamId || p.bmId || p.name}`, p.name])];
  const sel = $("#heatWho");
  const sig = JSON.stringify(opts);
  if (sel.dataset.sig !== sig) {
    sel.dataset.sig = sig;
    sel.innerHTML = opts.map(([v, l]) => `<option value="${esc(v)}">${esc(l)}</option>`).join("") || `<option value="">Track someone first</option>`;
    if (heatPick && opts.some((o) => o[0] === heatPick)) sel.value = heatPick;
    else {
      // start on whoever has the most play history
      const count = (v) => (v.startsWith("tag:") ? (tags[v.slice(4)] || { ps: [] }).ps : tr.filter((p) => `p:${p.steamId || p.bmId || p.name}` === v)).reduce((n, p) => n + (p.sessions || []).length, 0);
      const best = opts.reduce((a, o) => (count(o[0]) > count(a[0]) ? o : a), opts[0] || [""]);
      if (best && best[0]) sel.value = best[0];
    }
  }
  heatPick = sel.value;
  let who = [];
  if (heatPick.startsWith("tag:")) who = (tags[heatPick.slice(4)] || { ps: [] }).ps;
  else who = tr.filter((p) => `p:${p.steamId || p.bmId || p.name}` === heatPick);
  drawHeat(who);
  const gs = T.groups || [];
  $("#groupCount").textContent = gs.length ? `${gs.length} found` : "";
  $("#groupList").innerHTML = gs.map((g) => `<div class="grp"><div class="who">${g.members.map((m) => `<span class="${m.online ? "on" : ""} ${m.tracked ? "tg" : ""}" title="${m.online ? "Online now" : "Offline"}${m.tracked ? ", tracked" : ""}">${esc(m.name)}</span>`).join("")}</div>
    <div class="meta">Joined or left together ${g.together} times · ${g.members.filter((m) => m.online).length} of ${g.members.length} online now${g.members.some((m) => !m.tracked) ? ` · <a href="#" onclick="trackGroup(${gs.indexOf(g)});return false">Track the group</a>` : ""}</div></div>`).join("")
    || `<div class="empty-note">No groups spotted yet.</div>`;
};
$("#heatWho").addEventListener("change", (e) => { heatPick = e.target.value; renderPlayerExtras(); });
window.trackGroup = (i) => {
  const g = (S.tracker.groups || [])[i];
  if (!g) return;
  const tag = prompt("Tag for this group (e.g. their clan name):", "Group " + (i + 1));
  if (tag === null) return;
  const todo = g.members.filter((m) => !m.tracked);
  Promise.all(todo.map((m) => api("/api/tracker/track", { bmId: m.id.startsWith("n:") ? "" : m.id.replace(/^s:/, ""), steamId: m.id.startsWith("s:") ? m.id.slice(2) : "", name: m.name, tag, color: "#c9402c", notify: true })))
    .then(() => { toast("Tracking " + todo.length + " players", "Tagged " + tag); refresh(); }).catch((e) => toast("Couldn't track them", e.message, "alarm"));
};
function drawHeat(players) {
  const now = Date.now() / 1000, from = now - 14 * 86400;
  const grid = Array.from({ length: 7 }, () => new Array(24).fill(0));
  let any = false;
  // fraction of the last two weeks' (weekday, hour) slots in which any of them was online
  const seen = new Set(); // one count per calendar hour, however many of them were on
  for (const p of players) for (const s of p.sessions || []) {
    let a = Math.max(s.start, from), b = s.end || now;
    if (b <= a) continue;
    any = true;
    for (let t = Math.floor(a / 3600) * 3600; t < b; t += 3600) {
      const d = new Date(t * 1000), overlap = Math.min(b, t + 3600) - Math.max(a, t);
      if (overlap <= 600 || seen.has(t)) continue;
      seen.add(t);
      const wd = (d.getDay() + 6) % 7;
      grid[wd][d.getHours()] = Math.min(2, grid[wd][d.getHours()] + 1);
    }
  }
  const days = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];
  let h = `<div class="heat"><span></span>${Array.from({ length: 24 }, (_, i) => `<span class="hl">${i % 3 === 0 ? String(i).padStart(2, "0") : ""}</span>`).join("")}`;
  const today = (new Date().getDay() + 6) % 7, hourNow = new Date().getHours();
  days.forEach((d, di) => {
    h += `<span class="lab">${d}</span>`;
    for (let hr = 0; hr < 24; hr++) {
      const v = grid[di][hr];
      const bg = v === 0 ? "var(--slot)" : v === 1 ? "rgba(201,64,44,.45)" : "rgba(201,64,44,.95)";
      h += `<i style="background:${bg}${di === today && hr === hourNow ? ";outline:1px solid #e4dcd1" : ""}" title="${d} ${String(hr).padStart(2, "0")}:00 · online in ${v === 0 ? "neither" : v === 1 ? "one" : "both"} of the last two weeks"></i>`;
    }
  });
  h += `</div><div class="heatkey"><i style="background:var(--slot)"></i>never <i style="background:rgba(201,64,44,.45)"></i>one week <i style="background:rgba(201,64,44,.95)"></i>both weeks <span style="margin-left:auto">outlined: now</span></div>`;
  $("#heatmap").innerHTML = players.length ? h : `<div class="empty-note">Track players to see when they usually play.</div>`;
  $("#heatNote").textContent = players.length && !any ? "No play sessions recorded yet." : "";
}

/* ---------- server stats ---------- */
let statsAt = 0, statsData = null;
window.renderServerStats = async function () {
  if (Date.now() - statsAt > 60000 || !statsData) {
    statsAt = Date.now();
    try { statsData = await api("/api/stats"); } catch (e) { return; }
  }
  const v = statsData;
  const pts = v.points || [];
  const fmtH = (h) => `${String(h).padStart(2, "0")}:00`;
  $("#statSummary").innerHTML = chip(v.peak && v.peak.p ? `${v.peak.p}/${v.peak.m}` : "–", "Peak this week", "ok") +
    chip(v.busy && v.busy.length ? fmtH(v.busy[0]) : "–", "Busiest hour", "bad") + chip(v.quiet && v.quiet.length ? fmtH(v.quiet[0]) : "–", "Quietest hour", "info") +
    chip(v.forcedIn || "–", "To forced wipe", "warn");
  $("#popRange").textContent = pts.length ? `${v.daysOfData.toFixed(1)} days · every 10 min` : "";
  $("#popChart").innerHTML = pts.length > 2 ? popSVG(pts) : `<div class="empty-note">RustWatch saves the player count every 10 minutes while it's connected. The chart fills in from there.</div>`;
  const hs = (v.hours || []).filter((h) => h.n);
  $("#hourChart").innerHTML = hs.length ? hourSVG(v) : `<div class="empty-note">Not enough data yet.</div>`;
  $("#hourNote").textContent = v.quiet && v.quiet.length ? `Quietest: ${v.quiet.map(fmtH).join(", ")}. Busiest: ${v.busy.map(fmtH).join(", ")}. Times are your PC's.` : "";
  const fw = new Date(v.forced * 1000);
  $("#wipeBox").innerHTML = `<div class="wipeline"><b>Next forced wipe</b><span class="big">${esc(v.forcedIn)}</span><span class="muted">${fw.toLocaleString([], { weekday: "long", day: "numeric", month: "long", hour: "2-digit", minute: "2-digit" })}: Facepunch's monthly update wipes every server's map and blueprints (first Thursday, 19:00 UK).</span></div>
    ${v.wipedAt ? `<div class="wipeline"><b>This server wiped</b><span class="big">${esc(v.wipeAgo)}</span><span class="muted">${new Date(v.wipedAt * 1000).toLocaleString([], { weekday: "long", day: "numeric", month: "long", hour: "2-digit", minute: "2-digit" })}</span></div>` : ""}`;
};
function popSVG(pts) {
  const W = 1000, H = 230, L = 34, B = 22, T = 8;
  const t0 = pts[0].t, t1 = pts[pts.length - 1].t, max = Math.max(...pts.map((p) => p.m), 10);
  const x = (t) => L + ((t - t0) / Math.max(1, t1 - t0)) * (W - L - 6), y = (v) => T + (1 - v / max) * (H - T - B);
  let g = "";
  for (let k = 0; k <= 4; k++) { const v = Math.round((max * k) / 4); g += `<line class="grid" x1="${L}" x2="${W}" y1="${y(v)}" y2="${y(v)}"/><text x="${L - 6}" y="${y(v) + 4}" text-anchor="end">${v}</text>`; }
  // a label at each midnight
  const d = new Date(t0 * 1000); d.setHours(24, 0, 0, 0);
  for (let t = d.getTime() / 1000; t < t1; t += 86400) g += `<line class="grid" x1="${x(t)}" x2="${x(t)}" y1="${T}" y2="${H - B}"/><text x="${x(t) + 4}" y="${H - 6}">${new Date(t * 1000).toLocaleDateString([], { weekday: "short" })}</text>`;
  const line = pts.map((p, i) => `${i ? "L" : "M"}${x(p.t).toFixed(1)},${y(p.p).toFixed(1)}`).join("");
  const area = `${line}L${x(t1)},${y(0)}L${x(t0)},${y(0)}Z`;
  const cap = pts.map((p, i) => `${i ? "L" : "M"}${x(p.t).toFixed(1)},${y(p.m).toFixed(1)}`).join("");
  return `<svg viewBox="0 0 ${W} ${H}" role="img" aria-label="Players online over the last ${Math.round((t1 - t0) / 86400)} days">${g}<path class="area" d="${area}"/><path class="max" d="${cap}" fill="none"/><path class="line" d="${line}"/></svg>`;
}
function hourSVG(v) {
  const W = 1000, H = 200, L = 34, B = 22, T = 8;
  const max = Math.max(10, ...v.hours.map((h) => h.avg)), bw = (W - L) / 24;
  const y = (n) => T + (1 - n / max) * (H - T - B);
  let g = "";
  for (let k = 0; k <= 4; k++) { const n = Math.round((max * k) / 4); g += `<line class="grid" x1="${L}" x2="${W}" y1="${y(n)}" y2="${y(n)}"/><text x="${L - 6}" y="${y(n) + 4}" text-anchor="end">${n}</text>`; }
  v.hours.forEach((h) => {
    const cls = v.busy && v.busy.includes(h.hour) ? "busy" : v.quiet && v.quiet.includes(h.hour) ? "quiet" : "";
    const x = L + h.hour * bw + 3;
    if (h.n) g += `<rect class="bar ${cls}" x="${x}" y="${y(h.avg)}" width="${bw - 6}" height="${Math.max(1, y(0) - y(h.avg))}"><title>${String(h.hour).padStart(2, "0")}:00 · about ${Math.round(h.avg)} players</title></rect>`;
    if (h.hour % 3 === 0) g += `<text x="${x + (bw - 6) / 2}" y="${H - 6}" text-anchor="middle">${String(h.hour).padStart(2, "0")}</text>`;
  });
  const nh = new Date().getHours();
  g += `<line class="now" x1="${L + nh * bw + bw / 2}" x2="${L + nh * bw + bw / 2}" y1="${T}" y2="${H - B}"/>`;
  return `<svg viewBox="0 0 ${W} ${H}" role="img" aria-label="Average players by hour of the day">${g}</svg>`;
}

/* ---------- cameras ---------- */
let camTimer = null, camHeld = 0, camDrag = null, camInputTimer = null, camDX = 0, camDY = 0;
window.openCam = (id) => act("/api/cameras/open", { id }).then(() => { setTimeout(refresh, 400); }).catch(() => {});
window.closeCam = () => act("/api/cameras/close", {});
window.delCam = (id) => { if (confirm("Remove this camera from the list?")) act("/api/cameras/remove", { id }); };
window.renderCameras = function () {
  const C = S.cameras || { list: [] }, a = C.active;
  $("#camCount").textContent = C.list.length ? String(C.list.length) : "";
  $("#camList").innerHTML = C.list.map((c) => `<div class="camrow ${a && a.id === c.id ? "on" : ""}"><div><b>${esc(c.name)}</b><code>${esc(c.id)}</code></div>${a && a.id === c.id ? `<button class="ghost small" onclick="closeCam()">Close</button>` : `<button class="small" onclick="openCam('${esc(c.id)}')">Watch</button>`}<button class="danger small" onclick="delCam('${esc(c.id)}')">Remove</button></div>`).join("")
    || `<div class="empty-note pad">No cameras yet. Add one with the form below.</div>`;
  $("#camSummary").innerHTML = a ? chip(a.live ? "Live" : a.error ? "Error" : "Waiting", "Feed", a.live ? "ok" : a.error ? "bad" : "warn") + chip((a.players || []).length, "Players in view", (a.players || []).length ? "bad" : "") : "";
  $("#camTitle").innerHTML = a ? `${esc(a.name || a.id)} <span class="hmeta">${camKind(a.controls)}${a.width ? ` · ${a.width}×${a.height}` : ""}</span><button class="link" onclick="closeCam()">Close</button>` : "No camera open";
  const over = $("#camOver");
  if (!a) {
    over.innerHTML = `Pick a camera from the list to watch it.`;
    $("#camImg").removeAttribute("src");
  } else if (a.error) over.innerHTML = `<div class="err">${esc(a.error)}</div>`;
  else if (!a.frames) over.innerHTML = "Connecting to the camera…";
  else over.innerHTML = "";
  $$(".camscreen .rec, .camscreen .seen").forEach((e) => e.remove());
  if (a && a.live) $("#camScreen").insertAdjacentHTML("beforeend", `<span class="rec">Live</span>`);
  if (a && (a.players || []).length) $("#camScreen").insertAdjacentHTML("beforeend", `<span class="seen">In view: ${a.players.map(esc).join(", ")}</span>`);
  $("#camCross").hidden = !(a && a.controls & 32);
  const sig = a ? a.id + ":" + a.controls : "";
  if ($("#camCtl").dataset.sig !== sig) { $("#camCtl").dataset.sig = sig; $("#camCtl").innerHTML = a && !a.error ? camControls(a.controls) : ""; }
  // pull frames while this page shows an open camera
  if (a && !camTimer) camTimer = setInterval(camFrame, 250);
  if (!a && camTimer) { clearInterval(camTimer); camTimer = null; }
};
function camKind(f) {
  if (f & 1) return "Drone";
  if (f & 8) return "Auto turret";
  if (f & 2) return "PTZ camera";
  return "CCTV camera";
}
function camControls(f) {
  let h = "";
  if (f & 2) h += `<div class="dpad" aria-label="Look"><button class="ghost up" data-look="0,-12" aria-label="Look up">▲</button><button class="ghost left" data-look="-12,0" aria-label="Look left">◀</button><button class="ghost down" data-look="0,12" aria-label="Look down">▼</button><button class="ghost right" data-look="12,0" aria-label="Look right">▶</button></div>`;
  if (f & 1) h += `<div class="dpad" aria-label="Move"><button class="ghost up" data-btn="2" aria-label="Forward">W</button><button class="ghost left" data-btn="8" aria-label="Left">A</button><button class="ghost down" data-btn="4" aria-label="Back">S</button><button class="ghost right" data-btn="16" aria-label="Right">D</button></div>`;
  if (f & 4) h += `<button class="ghost" data-btn="128">Up</button><button class="ghost" data-btn="64">Down</button>`;
  if (f & 8) h += `<button class="danger" data-btn="1024">Fire</button>`;
  if (f & 16) h += `<button class="ghost" data-btn="8192">Reload</button>`;
  if ((f & 2) && !(f & 8) && !(f & 1)) h += `<button class="ghost" data-btn="1024" data-tap>Zoom</button>`;
  if (!f) return `<span class="hint">A fixed CCTV camera: no controls.</span>`;
  return h + `<span class="hint">${f & 2 ? "Drag the picture to look around. " : ""}${f & 1 ? "W A S D to fly, Space up, Ctrl down. " : ""}${f & 8 ? "Hold Fire to shoot. " : ""}Click the picture first for keyboard control.</span>`;
}
async function camFrame() {
  if (tab !== "cameras" || document.hidden) return;
  const a = S.cameras && S.cameras.active;
  if (!a || a.error || !a.frames) return;
  const img = $("#camImg");
  if (img.dataset.busy) return;
  img.dataset.busy = "1";
  const next = new Image();
  next.onload = () => { img.src = next.src; delete img.dataset.busy; };
  next.onerror = () => { delete img.dataset.busy; };
  next.src = "/api/cameras/frame?t=" + Date.now();
}
// held buttons and mouse look are sent together, about 5 times a second
function camSend() {
  if (!camHeld && !camDX && !camDY) { clearInterval(camInputTimer); camInputTimer = null; api("/api/cameras/input", { buttons: 0, dx: 0, dy: 0 }).catch(() => {}); return; }
  const b = { buttons: camHeld, dx: camDX, dy: camDY };
  camDX = camDY = 0;
  api("/api/cameras/input", b).catch(() => {});
}
function camKick() { if (!camInputTimer) { camSend(); camInputTimer = setInterval(camSend, 200); } }
const camCtl = $("#camCtl");
camCtl.addEventListener("pointerdown", (e) => {
  const b = e.target.closest("button");
  if (!b) return;
  if (b.dataset.look) { const [x, y] = b.dataset.look.split(",").map(Number); camDX += x; camDY += y; camKick(); b._rep = setInterval(() => { camDX += x; camDY += y; }, 200); }
  if (b.dataset.btn) {
    const v = +b.dataset.btn;
    if ("tap" in b.dataset) { api("/api/cameras/input", { buttons: v, dx: 0, dy: 0 }).then(() => api("/api/cameras/input", { buttons: 0, dx: 0, dy: 0 })).catch(() => {}); return; }
    camHeld |= v; camKick();
  }
  b.setPointerCapture(e.pointerId);
});
const camRelease = (e) => {
  const b = e.target.closest("button");
  if (!b) return;
  clearInterval(b._rep);
  if (b.dataset.btn) camHeld &= ~+b.dataset.btn;
};
camCtl.addEventListener("pointerup", camRelease);
camCtl.addEventListener("pointercancel", camRelease);
const scr = $("#camScreen");
scr.addEventListener("pointerdown", (e) => { scr.focus(); camDrag = { x: e.clientX, y: e.clientY }; scr.classList.add("drag"); scr.setPointerCapture(e.pointerId); });
scr.addEventListener("pointermove", (e) => {
  if (!camDrag) return;
  camDX += (e.clientX - camDrag.x) * 0.6; camDY += (e.clientY - camDrag.y) * 0.6;
  camDrag = { x: e.clientX, y: e.clientY };
  camKick();
});
scr.addEventListener("pointerup", () => { camDrag = null; scr.classList.remove("drag"); });
const CAM_KEYS = { w: 2, s: 4, a: 8, d: 16, " ": 128, control: 64, r: 8192 };
scr.addEventListener("keydown", (e) => {
  const k = CAM_KEYS[e.key.toLowerCase()];
  const look = { arrowleft: [-14, 0], arrowright: [14, 0], arrowup: [0, -14], arrowdown: [0, 14] }[e.key.toLowerCase()];
  if (look) { camDX += look[0]; camDY += look[1]; camKick(); e.preventDefault(); return; }
  if (k) { camHeld |= k; camKick(); e.preventDefault(); }
});
scr.addEventListener("keyup", (e) => { const k = CAM_KEYS[e.key.toLowerCase()]; if (k) camHeld &= ~k; });
scr.addEventListener("blur", () => { camHeld = 0; });
$("#camForm").addEventListener("submit", (e) => {
  e.preventDefault();
  const f = e.target, id = f.id.value.trim();
  act("/api/cameras/save", { id, name: f.name.value.trim() }, "Camera added").then(() => { f.reset(); openCam(id); }).catch(() => {});
});

/* ---------- raid costs ---------- */
let rc = null, rcPlan = [];
try { rcPlan = JSON.parse(localStorage.getItem("rw.raidplan") || "[]"); } catch (e) { rcPlan = []; }
const RC_KINDS = [["c4", "C4"], ["rockets", "Rockets"], ["satchels", "Satchels"], ["ammo", "Explo ammo"], ["beancans", "Beancans"]];
window.renderRaidCost = async function () {
  if (!rc) { try { rc = await api("/api/raidcost"); } catch (e) { return; } }
  $("#rcAsOf").innerHTML = `<span class="muted small">Numbers from the game files of ${esc(rc.asOf)}</span>`;
  const sul = rc.sulfur;
  $("#rcTable").innerHTML = `<table class="rctable"><thead><tr><th>Target</th><th class="n">HP</th>${RC_KINDS.slice(0, 4).map(([, l]) => `<th class="n">${l}</th>`).join("")}</tr></thead><tbody>${rc.targets.map((t) => {
    const costs = RC_KINDS.slice(0, 4).map(([k]) => t[k] * sul[k]);
    const best = Math.min(...costs);
    return `<tr><td>${esc(t.name)}</td><td class="n">${t.hp.toLocaleString()}</td>${RC_KINDS.slice(0, 4).map(([k], i) => `<td class="n ${costs[i] === best ? "best" : ""}">${t[k]}<small>${costs[i].toLocaleString()} sulfur</small></td>`).join("")}</tr>`;
  }).join("")}</tbody></table>`;
  const sel = $("#rcAdd");
  if (!sel.options.length) sel.innerHTML = rc.targets.map((t) => `<option value="${t.key}">${esc(t.name)}</option>`).join("");
  drawPlan();
};
function drawPlan() {
  if (!rc) return;
  const byKey = Object.fromEntries(rc.targets.map((t) => [t.key, t]));
  rcPlan = rcPlan.filter((p) => byKey[p.key]);
  try { localStorage.setItem("rw.raidplan", JSON.stringify(rcPlan)); } catch (e) {}
  $("#rcPlan").innerHTML = rcPlan.map((p, i) => `<div class="rcrow"><span>${esc(byKey[p.key].name)}</span><input type="number" min="1" max="200" value="${p.n}" aria-label="How many ${esc(byKey[p.key].name)}" onchange="rcSet(${i}, this.value)"><button class="ghost small" onclick="rcDel(${i})">Remove</button></div>`).join("")
    || `<div class="empty-note">Add the walls and doors between you and the loot.</div>`;
  if (!rcPlan.length) { $("#rcTotals").innerHTML = ""; return; }
  const tot = RC_KINDS.map(([k, l]) => { const n = rcPlan.reduce((a, p) => a + byKey[p.key][k] * p.n, 0); return { k, l, n, s: n * rc.sulfur[k] }; });
  // the cheapest mix picks the cheapest explosive for each target
  let mixS = 0; const mix = {};
  rcPlan.forEach((p) => { const t = byKey[p.key]; const [k] = RC_KINDS.slice(0, 4).map(([k]) => k).sort((a, b) => t[a] * rc.sulfur[a] - t[b] * rc.sulfur[b]); mix[k] = (mix[k] || 0) + t[k] * p.n; mixS += t[k] * p.n * rc.sulfur[k]; });
  const best = Math.min(...tot.slice(0, 4).map((x) => x.s));
  $("#rcTotals").innerHTML = `<div class="rctot">${tot.slice(0, 4).map((x) => `<div class="${x.s === best ? "best" : ""}"><span>${x.l} only</span><b>${x.n.toLocaleString()}</b><small>${x.s.toLocaleString()} sulfur</small></div>`).join("")}
    <div class="best"><span>Cheapest mix</span><b>${mixS.toLocaleString()}</b><small>${Object.entries(mix).map(([k, n]) => `${n} ${RC_KINDS.find((r) => r[0] === k)[1].toLowerCase()}`).join(" + ")}</small></div></div>`;
}
window.rcSet = (i, v) => { rcPlan[i].n = Math.max(1, Math.min(200, parseInt(v) || 1)); drawPlan(); };
window.rcDel = (i) => { rcPlan.splice(i, 1); drawPlan(); };
$("#rcAddBtn").addEventListener("click", () => { const k = $("#rcAdd").value; const ex = rcPlan.find((p) => p.key === k); if (ex) ex.n++; else rcPlan.push({ key: k, n: 1 }); drawPlan(); });
$("#rcClear").addEventListener("click", () => { rcPlan = []; drawPlan(); });
