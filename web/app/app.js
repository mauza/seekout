/* seekout app — hash-routed views over the PocketBase REST + realtime API */
"use strict";

const pb = new PocketBase("/");
// PocketBase sends no cache headers, so a GET issued right after a mutation
// can be answered from the browser cache with stale rows. Opt every request
// out explicitly.
pb.beforeSend = (url, options) => {
  options.cache = "no-store";
  return { url, options };
};
// Views issue parallel requests to one collection (counts, pages), which
// the SDK's auto-cancellation would abort as duplicates.
pb.autoCancellation(false);
const $ = (id) => document.getElementById(id);

// The API speaks in seekers and alerters; the UI calls them searches and
// notifications, which is what they are to someone using it.
const OPS = ["=", "!=", ">", "<", ">=", "<=", "in", "contains"];
const OP_LABELS = {
  "=": "is", "!=": "is not", ">": "more than", "<": "less than",
  ">=": "at least", "<=": "at most", in: "is one of", contains: "contains",
};
// Soft conditions trade off against each other (see internal/match); only
// ordering ops can be soft, since they say which direction is better.
const SOFT_OPS = [">", ">=", "<", "<="];
const LISTING_FIELDS = ["title", "price", "url", "kind", "description", "external_id", "published",
  "deal.pct_below", "deal.unit_price", "deal.typical", "deal.confidence", "deal.item_name"];
const PAGE = 30;

const state = {
  sources: [],
  alerters: [],
  seekers: [],
  attrKeys: [],
  unseen: 0,
  alertSeeker: "",
  listingFilter: "",
  listingQuery: "",
  pollResults: {},
  // Source and alerter types with their config fields, and the listing
  // kinds, from /api/seekout/plugins: the forms below are built from it,
  // so a type registered in Go shows up here with no UI change.
  plugins: { sources: [], alerters: [], kinds: [] },
};

/* ------------------------------------------------------------- utilities -- */

const esc = (s) =>
  String(s ?? "").replace(/[&<>"']/g, (c) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));

function el(html) {
  const t = document.createElement("template");
  t.innerHTML = html.trim();
  return t.content.firstElementChild;
}

const parseTime = (iso) => new Date(iso.replace(" ", "T") + (iso.endsWith("Z") ? "" : "Z"));

function timeAgo(iso) {
  if (!iso) return "never";
  const secs = (Date.now() - parseTime(iso).getTime()) / 1000;
  if (Number.isNaN(secs)) return "unknown";
  if (secs < 45) return "just now";
  const units = [["m", 60], ["h", 3600], ["d", 86400], ["mo", 2592000]];
  let out = `${Math.round(secs / 60)}m ago`;
  for (const [suffix, size] of units) {
    if (secs >= size) out = `${Math.floor(secs / size)}${suffix} ago`;
  }
  return out;
}

function absTime(iso) {
  if (!iso) return "";
  const d = parseTime(iso);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleString();
}

const money = (n) => (n > 0 ? `$${Number(n).toLocaleString()}` : "");

// fmtValue renders a condition value: prices as money, big numbers with
// separators, lists comma-joined.
function fmtValue(field, v) {
  if (Array.isArray(v)) return v.join(", ");
  const n = typeof v === "number" ? v : typeof v === "string" && v.trim() !== "" ? Number(v) : NaN;
  if (!Number.isNaN(n)) {
    if (field === "price" || field.endsWith("_price")) return money(n) || "$0";
    return Math.abs(n) >= 10000 ? n.toLocaleString() : String(n);
  }
  return String(v ?? "");
}

const plural = (n, word) => `${n} ${word}${n === 1 ? "" : /(s|ch|sh|x)$/.test(word) ? "es" : "s"}`;

function everyLabel(minutes) {
  const m = Number(minutes) || 0;
  if (m === 1440) return "daily";
  if (m === 60) return "hourly";
  if (m > 60 && m % 60 === 0) return `every ${m / 60}h`;
  return `every ${m} min`;
}

function toast(message, kind = "") {
  const node = el(`<div class="toast ${kind}">${esc(message)}</div>`);
  $("toasts").append(node);
  setTimeout(() => {
    node.classList.add("leaving");
    setTimeout(() => node.remove(), 200);
  }, 4200);
}

function apiError(err) {
  const data = err?.response?.data;
  if (data && typeof data === "object") {
    const first = Object.entries(data)[0];
    if (first) {
      const [field, detail] = first;
      return `${field}: ${detail && typeof detail === "object" ? detail.message : detail}`;
    }
  }
  return err?.response?.message || err?.message || "Request failed";
}

/* ---------------------------------------------------------- listing facts -- */

// Common listing attrs get a compact label on cards; every attr is still
// listed in a listing's details. Sources add attrs freely, so anything not
// named here simply isn't summarized.
const FACTS = [
  ["beds", (v) => `${v} bd`],
  ["baths", (v) => `${v} ba`],
  ["sqft", (v) => `${Number(v).toLocaleString()} sqft`],
  ["garage_capacity", (v) => (Number(v) > 0 ? `${v}-car garage` : "no garage")],
  ["lot_acres", (v) => (Number(v) > 0 ? `${v} ac lot` : "")],
  ["hoa", (v, a) => (v === false ? "no HOA" : v === true ? `HOA${a.hoa_fee ? ` ${a.hoa_fee}` : ""}` : "")],
  ["miles", (v) => `${Number(v).toLocaleString()} mi`],
  ["year", (v) => String(v)],
];

function factChips(attrs) {
  if (!attrs) return "";
  const out = [];
  for (const [key, fmt] of FACTS) {
    if (attrs[key] === undefined || attrs[key] === null || attrs[key] === "") continue;
    const label = fmt(attrs[key], attrs);
    if (label) out.push(`<span class="fact">${esc(label)}</span>`);
  }
  return out.join("");
}

function statusBadge(attrs) {
  const s = attrs?.status;
  if (!s) return "";
  const tone = /^(ACTIVE|COMING SOON)$/.test(s) ? "badge-ok" : /SOLD|WITHDRAWN|OFF MARKET|EXPIRED/.test(s) ? "badge-err" : "badge-warn";
  return `<span class="badge ${tone}">${esc(s.toLowerCase())}</span>`;
}

// perUnit formats a unit price: "$9.38/GB", "$650 each".
function perUnit(p, unit) {
  if (typeof p !== "number") return "";
  const s = p >= 100 ? `$${Math.round(p).toLocaleString()}` : `$${p.toFixed(2)}`;
  return !unit || unit === "each" ? `${s} each` : `${s}/${unit}`;
}

// dealChip shows how a priced listing compares with the typical price for
// its item (attrs.deal, written by the pricing step).
function dealChip(attrs) {
  const d = attrs?.deal;
  if (!d || typeof d.pct_below !== "number") return "";
  const good = d.pct_below >= DEAL_DEFAULT && d.confidence !== "low";
  const pct = Math.round(Math.abs(d.pct_below));
  const label = d.pct_below >= 0.5 ? `${pct}% under typical` : d.pct_below <= -0.5 ? `${pct}% over typical` : "typical price";
  const title = `${perUnit(d.unit_price, d.unit)} vs typical ${perUnit(d.typical, d.unit)} for ${d.item_name} ` +
    `(${d.basis}, ${d.comps} comps, ${d.confidence} confidence)`;
  return `<span class="deal ${good ? "deal-good" : ""}" title="${esc(title)}">${esc(label)}</span>`;
}

// thumb is a listing photo; a broken one becomes a blank tile. Cards leave
// it out entirely for imageless listings (placeholder=false).
function thumb(url, placeholder = true) {
  if (!url) return placeholder ? `<div class="thumb thumb-empty"></div>` : "";
  return `<img class="thumb" src="${esc(url)}" alt="" loading="lazy" onerror="this.replaceWith(Object.assign(document.createElement('div'),{className:'thumb thumb-empty'}))">`;
}

/* ---------------------------------------------------------------- modals -- */

// openModal shows a form dialog. onSubmit may throw to keep it open with
// the error shown; onClose runs however it closes, with whether it was
// submitted.
function openModal({ title, body, submitLabel = "Save", onSubmit, onClose, wide = false, danger = false, footerExtra = null }) {
  const backdrop = el(`
    <div class="modal-backdrop">
      <div class="modal ${wide ? "modal-wide" : ""}" role="dialog" aria-modal="true">
        <header>
          <h2>${esc(title)}</h2>
          <button class="btn btn-icon btn-ghost" type="button" data-close aria-label="Close">
            <svg viewBox="0 0 24 24" width="16" height="16"><path d="m6 6 12 12M18 6 6 18" stroke="currentColor" stroke-width="2" stroke-linecap="round"/></svg>
          </button>
        </header>
        <form novalidate>
          <div class="body"></div>
          <footer>
            <p class="err-text"></p>
            <button type="button" class="btn" data-close>Cancel</button>
            <button type="submit" class="btn ${danger ? "btn-danger-solid" : "btn-primary"}">${esc(submitLabel)}</button>
          </footer>
        </form>
      </div>
    </div>`);

  const form = backdrop.querySelector("form");
  const bodyEl = backdrop.querySelector(".body");
  const errEl = backdrop.querySelector(".err-text");
  bodyEl.append(body);
  if (footerExtra) backdrop.querySelector("footer").prepend(footerExtra);

  let submitted = false;
  const close = () => {
    if (!backdrop.isConnected) return;
    backdrop.remove();
    document.removeEventListener("keydown", onKey);
    onClose?.(submitted);
  };
  // Only the topmost dialog answers Escape.
  const onKey = (e) => e.key === "Escape" && $("modal-host").lastElementChild === backdrop && close();
  backdrop.close = close;

  backdrop.querySelectorAll("[data-close]").forEach((b) => b.addEventListener("click", close));
  backdrop.addEventListener("mousedown", (e) => e.target === backdrop && close());
  document.addEventListener("keydown", onKey);

  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    // Native validation, but reported here so the message sits by the
    // buttons instead of in a browser bubble that can be off screen.
    const invalid = form.querySelector(":invalid");
    if (invalid) {
      invalid.focus();
      const label = invalid.closest(".field")?.querySelector("label")?.firstChild?.textContent?.trim();
      errEl.textContent = `${label ? `${label}: ` : ""}${invalid.validationMessage}`;
      return;
    }
    const submit = form.querySelector('[type="submit"]');
    submit.disabled = true;
    errEl.textContent = "";
    try {
      await onSubmit(bodyEl);
      submitted = true;
      close();
    } catch (err) {
      errEl.textContent = apiError(err);
      submit.disabled = false;
    }
  });

  $("modal-host").append(backdrop);
  bodyEl.querySelector("input:not([type=checkbox]), select, textarea")?.focus();
  return { close };
}

const closeModals = () => [...$("modal-host").children].forEach((m) => m.close?.());

function confirmDialog(title, message, confirmLabel = "Delete") {
  return new Promise((resolve) => {
    openModal({
      title,
      body: el(`<p class="muted">${esc(message)}</p>`),
      submitLabel: confirmLabel,
      danger: true,
      onSubmit: async () => {},
      onClose: resolve,
    });
  });
}

function switchField(label, checked) {
  return el(`
    <label class="row switch-row">
      <span class="switch"><input type="checkbox" ${checked ? "checked" : ""}><span class="track"></span></span>
      <span class="small muted">${esc(label)}</span>
    </label>`);
}

/* ------------------------------------------------------------------ data -- */

async function loadAll() {
  const [sources, alerters, seekers, plugins] = await Promise.all([
    pb.collection("sources").getFullList({ sort: "name" }),
    pb.collection("alerters").getFullList({ sort: "name" }),
    pb.collection("seekers").getFullList({ sort: "name" }),
    state.plugins.kinds.length ? state.plugins : pb.send("/api/seekout/plugins", {}),
  ]);
  state.sources = sources;
  state.alerters = alerters;
  state.seekers = seekers;
  state.plugins = plugins;
}

// Attr keys seen on recent listings, for the condition field suggestions.
// Bare keys: the matcher paths straight into attrs, so a suggested
// "attrs.beds" would resolve to attrs["attrs"]["beds"] and never match.
async function loadAttrKeys() {
  const res = await pb.collection("listings").getList(1, 200, { sort: "-created", fields: "attrs", skipTotal: true });
  const keys = new Set(state.attrKeys);
  for (const l of res.items) for (const k of Object.keys(l.attrs || {})) keys.add(k);
  state.attrKeys = [...keys].sort();
}

const sourceName = (id) => state.sources.find((s) => s.id === id)?.name || "unknown source";
const alerterName = (id) => state.alerters.find((a) => a.id === id)?.name || "deleted notification";

/* ---------------------------------------------------------- plugin forms -- */

// Admins manage what every user shares: sources and invites. The API
// enforces it; this only hides controls that would fail.
const isAdmin = () => Boolean(pb.authStore.record?.admin);

const pluginInfo = (group, type) => state.plugins[group].find((p) => p.type === type);

// configForm renders one input per declared config field (internal/plugin)
// for one type. Only the selected type's fields are in the DOM, so a
// required input is never hidden (a hidden required control blocks submit
// with nothing to focus).
function configForm(info, config) {
  const box = el(`<div class="stack"></div>`);
  if (!info) return box;
  if (info.description) box.append(el(`<p class="hint">${esc(info.description)}</p>`));
  const grid = el(`<div class="cfg-grid"></div>`);
  for (const f of info.fields) {
    const v = config?.[f.key] ?? (f.type === "select" || f.type === "number" ? f.default ?? "" : "");
    const hint = f.hint || (f.required ? "" : "optional");
    const attrs = `data-cfg="${esc(f.key)}" ${f.required ? "required" : ""}`;
    let input;
    if (f.type === "select") {
      input = `<select ${attrs}>${f.options.map((o) =>
        `<option ${o === v ? "selected" : ""}>${esc(o)}</option>`).join("")}</select>`;
    } else {
      const type = { url: "url", number: "number", secret: "password" }[f.type] || "text";
      const range = `${f.min != null ? `min="${f.min}"` : ""} ${f.max != null ? `max="${f.max}"` : ""}`;
      input = `<input ${attrs} type="${type}" ${range} autocomplete="off" spellcheck="false"
        placeholder="${esc(f.placeholder || "")}" value="${esc(v)}">`;
    }
    grid.append(el(`
      <div class="field ${f.type === "url" ? "cfg-wide" : ""}">
        <label>${esc(f.label)}${hint ? ` <span class="hint">${esc(hint)}</span>` : ""}</label>
        ${input}
      </div>`));
  }
  box.append(grid);
  return box;
}

// readConfig returns base with the form's fields applied. Keys the form
// doesn't offer (a webhook's headers, set over the API) are kept; a field
// left blank is removed so the type's default applies.
function readConfig(box, info, base) {
  const out = { ...base };
  for (const f of info?.fields || []) {
    const raw = box.querySelector(`[data-cfg="${CSS.escape(f.key)}"]`).value.trim();
    if (raw === "") delete out[f.key];
    else out[f.key] = f.type === "number" ? Number(raw) : raw;
  }
  return out;
}

// configSummary is the one-line description on a source or notification
// card. Secrets are left out.
function configSummary(info, config) {
  if (!info) return "";
  return info.fields
    .filter((f) => f.type !== "secret")
    .map((f) => {
      const v = config?.[f.key] ?? f.default;
      if (v === undefined || v === "") return "";
      return f.type === "number" ? `${f.label.toLowerCase()} ${v}` : String(v);
    })
    .filter(Boolean)
    .join(" · ");
}

// typePicker builds the type picker plus the config section that follows
// it, re-rendering the section when the type changes. Returns the picker
// and a read() that gives the config to save.
function typePicker(group, record) {
  const infos = state.plugins[group];
  const current = record?.type || infos[0]?.type;
  const select = el(`<select name="type">${infos.map((p) =>
    `<option value="${esc(p.type)}" ${p.type === current ? "selected" : ""}>${esc(p.label)}</option>`).join("")}</select>`);
  const section = el(`<div></div>`);
  // Editing keeps the record's config (and keys the form doesn't offer)
  // as long as the type stays the same.
  const baseFor = (type) => (record && record.type === type ? record.config || {} : {});
  const render = () => section.replaceChildren(configForm(pluginInfo(group, select.value), baseFor(select.value)));
  select.addEventListener("change", render);
  render();
  return {
    select,
    section,
    read: () => readConfig(section, pluginInfo(group, select.value), baseFor(select.value)),
  };
}

/* ----------------------------------------------------------------- views -- */

function setHead(title, sub, actions = []) {
  $("page-title").textContent = title;
  $("page-sub").textContent = sub;
  $("page-actions").replaceChildren(...actions);
  document.title = `${title} · seekout`;
}

function emptyState(icon, heading, text, action) {
  const node = el(`
    <div class="empty">
      ${icon}
      <h3>${esc(heading)}</h3>
      <p>${esc(text)}</p>
    </div>`);
  if (action) node.append(action);
  return node;
}

function button(label, cls, onClick) {
  const b = el(`<button type="button" class="btn ${cls}">${label}</button>`);
  b.addEventListener("click", onClick);
  return b;
}

const ICON_BELL = `<svg viewBox="0 0 24 24"><path d="M12 3a6 6 0 0 0-6 6c0 5-2 6-2 6h16s-2-1-2-6a6 6 0 0 0-6-6Z" stroke="currentColor" stroke-width="1.8" fill="none" stroke-linejoin="round"/><path d="M10 19a2 2 0 0 0 4 0" stroke="currentColor" stroke-width="1.8" fill="none" stroke-linecap="round"/></svg>`;
const ICON_SEARCH = `<svg viewBox="0 0 24 24"><circle cx="11" cy="11" r="6" stroke="currentColor" stroke-width="1.8" fill="none"/><path d="m15.5 15.5 4 4" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/></svg>`;
const ICON_FEED = `<svg viewBox="0 0 24 24"><path d="M4 11a9 9 0 0 1 9 9M4 5a15 15 0 0 1 15 15" stroke="currentColor" stroke-width="1.8" fill="none" stroke-linecap="round"/><circle cx="5" cy="19" r="1.6" fill="currentColor"/></svg>`;
const ICON_LIST = `<svg viewBox="0 0 24 24"><rect x="4" y="5" width="16" height="14" rx="2.5" stroke="currentColor" stroke-width="1.8" fill="none"/><path d="M8 10h8M8 14h5" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/></svg>`;
const ICON_SEND = `<svg viewBox="0 0 24 24"><path d="M4 12 20 4l-6 16-3-7-7-1Z" stroke="currentColor" stroke-width="1.8" fill="none" stroke-linejoin="round"/></svg>`;
const ICON_MAIL = `<svg viewBox="0 0 24 24"><path d="M4 7h16v10H4z" stroke="currentColor" stroke-width="1.8" fill="none" stroke-linejoin="round"/><path d="m4 8 8 5 8-5" stroke="currentColor" stroke-width="1.8" fill="none" stroke-linejoin="round"/></svg>`;
const ICON_CHECK = `<svg viewBox="0 0 24 24"><path d="m5 12.5 4.5 4.5L19 7.5" stroke="currentColor" stroke-width="2.2" fill="none" stroke-linecap="round" stroke-linejoin="round"/></svg>`;

function skeletons(n = 3) {
  const box = el(`<div class="view-inner"></div>`);
  for (let i = 0; i < n; i++) box.append(el(`<div class="skeleton"></div>`));
  return box;
}

// loadMore appends a "Show more" button that fetches the next page.
function loadMore(box, fetchPage, renderItem, page, totalPages) {
  if (page >= totalPages) return;
  const more = button("Show more", "btn-ghost load-more", async () => {
    more.disabled = true;
    const res = await fetchPage(page + 1);
    more.remove();
    for (const item of res.items) box.append(renderItem(item));
    loadMore(box, fetchPage, renderItem, res.page, res.totalPages);
  });
  box.append(more);
}

/* ---- setup checklist ---- */

// setupChecklist walks a new user through the three things that make
// alerts arrive, and disappears once they are done.
function setupChecklist() {
  const hasAlerter = state.alerters.length > 0;
  const hasSeeker = state.seekers.length > 0;
  const wired = state.seekers.some((s) => s.enabled && s.alerters?.length);
  if (hasAlerter && hasSeeker && wired) return null;

  const step = (done, title, text, action) => {
    const li = el(`
      <li class="${done ? "done" : ""}">
        <span class="step-mark">${done ? ICON_CHECK : ""}</span>
        <div class="grow"><strong>${esc(title)}</strong><p class="small muted">${esc(text)}</p></div>
      </li>`);
    if (!done && action) li.append(action);
    return li;
  };
  const box = el(`
    <section class="card setup">
      <h3>Get set up</h3>
      <p class="small muted">Three steps and new listings come to you.</p>
      <ol class="setup-steps"></ol>
    </section>`);
  const ol = box.querySelector("ol");
  ol.append(
    step(hasAlerter, "Choose where to be notified",
      "A phone push through ntfy, or a webhook into Slack, Discord or anything else.",
      button("Add notification", "btn-sm btn-primary", () => alerterModal(null))),
    step(hasSeeker, "Describe what you're looking for",
      "A search lists what a listing must have, and what would be nice. You'll see what it would have caught as you type.",
      button("New search", `btn-sm ${hasAlerter ? "btn-primary" : ""}`, () => seekerModal(null))),
    step(wired, "Connect them",
      "Pick the notification in your search. Without one, matches only show up on this page.",
      hasSeeker && hasAlerter
        ? button("Edit search", "btn-sm btn-primary", () => seekerModal(state.seekers.find((s) => s.enabled) || state.seekers[0]))
        : null),
  );
  return box;
}

/* ---- matches (alerts) ---- */

// whyLine pulls the soft-score explanation out of an alert body (the
// pipeline writes it as the line starting "score").
function whyLine(body) {
  return String(body || "").split("\n").find((l) => l.startsWith("score ")) || "";
}

function deliveryBadges(a) {
  const delivered = Array.isArray(a.delivered) ? a.delivered : [];
  if (!delivered.length) {
    return `<span class="badge" title="This search has no notification, so the match only shows here">not sent</span>`;
  }
  return delivered
    .map((d) => d.ok
      ? `<span class="badge badge-ok badge-dot" title="Delivered">${esc(d.name || "sent")}</span>`
      : `<span class="badge badge-err badge-dot" title="${esc(d.error || "failed")}">${esc(d.name || "notification")} failed</span>`)
    .join("");
}

function alertCard(a) {
  const listing = a.expand?.listing;
  const seeker = a.expand?.seeker;
  const why = whyLine(a.body);
  const score = why.match(/^score ([+-][\d.]+)/)?.[1];
  const url = a.url || listing?.url || "";
  const card = el(`
    <article class="card listing-card">
      ${thumb(listing?.image, false)}
      <div class="listing-main">
        <div class="listing-top">
          <a class="item-title" ${url ? `href="${esc(url)}" target="_blank" rel="noopener"` : ""}>${esc(a.title || "(untitled listing)")}</a>
          ${listing?.price > 0 ? `<span class="price tnum">${esc(money(listing.price))}</span>` : ""}
        </div>
        <div class="facts">${statusBadge(listing?.attrs)}${factChips(listing?.attrs)}</div>
        <div class="item-meta">
          ${seeker ? `<a class="badge badge-accent" href="#/alerts" data-seeker="${esc(seeker.id)}">${esc(seeker.name)}</a>` : ""}
          ${score ? `<span class="score" title="${esc(why)}">score ${esc(score)}</span>` : ""}
          ${listing ? `<span>${esc(sourceName(listing.source))}</span>` : ""}
          <span title="${esc(absTime(a.created))}">${esc(timeAgo(a.created))}</span>
          <span class="item-badges">${deliveryBadges(a)}</span>
        </div>
      </div>
    </article>`);
  card.querySelector("[data-seeker]")?.addEventListener("click", (e) => {
    e.preventDefault();
    state.alertSeeker = seeker.id;
    route();
  });
  return card;
}

const alertsPage = (page) => pb.collection("alerts").getList(page, PAGE, {
  sort: "-created",
  expand: "seeker,listing",
  filter: state.alertSeeker ? pb.filter("seeker = {:s}", { s: state.alertSeeker }) : "",
});

async function viewAlerts() {
  const actions = [];
  if (state.seekers.length > 1 || state.alertSeeker) {
    const filter = el(`
      <select class="select-auto" aria-label="Filter by search">
        <option value="">All searches</option>
        ${state.seekers.map((s) => `<option value="${esc(s.id)}" ${state.alertSeeker === s.id ? "selected" : ""}>${esc(s.name)}</option>`).join("")}
      </select>`);
    filter.addEventListener("change", () => {
      state.alertSeeker = filter.value;
      route();
    });
    actions.push(filter);
  }
  setHead("Matches", "New listings your searches caught, newest first", actions);
  $("view").replaceChildren(skeletons());

  const weekAgo = new Date(Date.now() - 7 * 86400e3).toISOString().replace("T", " ");
  const [res, week] = await Promise.all([
    alertsPage(1),
    pb.collection("alerts").getList(1, 1, { filter: pb.filter("created >= {:t}", { t: weekAgo }), fields: "id" }),
  ]);
  state.unseen = 0;
  renderNavCount();

  const box = el(`<div class="view-inner"></div>`);
  const setup = setupChecklist();
  if (setup) box.append(setup);

  const activeSeekers = state.seekers.filter((s) => s.enabled).length;
  box.append(el(`
    <div class="stats">
      <div class="card stat"><div class="n tnum">${week.totalItems}</div><div class="k">matches this week</div></div>
      <div class="card stat"><div class="n tnum" id="stat-alerts">${res.totalItems}</div><div class="k">${state.alertSeeker ? "from this search" : "matches in all"}</div></div>
      <a class="card stat card-hover" href="#/seekers"><div class="n tnum">${activeSeekers}</div><div class="k">active searches</div></a>
    </div>`));

  if (!res.items.length) {
    const cta = state.seekers.length ? null : button("New search", "btn-primary", () => seekerModal(null));
    box.append(emptyState(ICON_BELL, state.alertSeeker ? "No matches from this search yet" : "No matches yet",
      state.seekers.length
        ? "When a new listing fits one of your searches it shows up here the moment it's found, and goes to your notifications."
        : "Create a search and new listings that fit it will show up here.", cta));
  } else {
    const list = el(`<div class="stack-sm" id="alert-list"></div>`);
    for (const a of res.items) list.append(alertCard(a));
    loadMore(list, alertsPage, alertCard, res.page, res.totalPages);
    box.append(list);
  }
  $("view").replaceChildren(box);
}

/* ---- searches (seekers) ---- */

function conditionRow(cond = { field: "", op: "=", value: "" }) {
  const value = Array.isArray(cond.value) ? cond.value.join(",") : cond.value ?? "";
  const num = (v) => (v === undefined || v === null ? "" : v);
  const row = el(`
    <div class="cond-row">
      <div class="cond-main">
        <input data-k="field" type="text" list="field-suggestions" placeholder="field, e.g. price" aria-label="Field" value="${esc(cond.field)}" autocomplete="off" spellcheck="false">
        <select data-k="op" aria-label="Comparison">${OPS.map((o) => `<option value="${esc(o)}" ${o === cond.op ? "selected" : ""}>${esc(OP_LABELS[o])}</option>`).join("")}</select>
        <input data-k="value" type="text" placeholder="value" aria-label="Value" value="${esc(value)}" autocomplete="off">
        <div class="cond-mode" role="group" aria-label="Must have or flexible">
          <button type="button" data-mode="hard" class="${cond.soft ? "" : "on"}">Must</button>
          <button type="button" data-mode="soft" class="${cond.soft ? "on" : ""}" title="Flexible: can trade off against other flexible conditions. Only for at least / at most / more than / less than.">Flexible</button>
        </div>
        <button type="button" class="btn btn-icon btn-ghost" data-remove aria-label="Remove condition">
          <svg viewBox="0 0 24 24" width="15" height="15"><path d="m6 6 12 12M18 6 6 18" stroke="currentColor" stroke-width="2" stroke-linecap="round"/></svg>
        </button>
      </div>
      <div class="soft-fields" ${cond.soft ? "" : "hidden"}>
        <label><span data-floor-label>Accept down to</span>
          <input data-k="floor" type="number" step="any" value="${esc(num(cond.floor))}"></label>
        <label>Ideal
          <input data-k="best" type="number" step="any" value="${esc(num(cond.best))}"></label>
        <label>Importance
          <input data-k="weight" type="number" step="any" min="0" placeholder="1" value="${esc(cond.weight && cond.weight !== 1 ? cond.weight : "")}"></label>
      </div>
    </div>`);
  const q = (k) => row.querySelector(`[data-k="${k}"]`);
  let soft = Boolean(cond.soft);
  const sync = () => {
    const ordering = SOFT_OPS.includes(q("op").value);
    if (!ordering) soft = false;
    row.querySelector('[data-mode="soft"]').disabled = !ordering;
    row.querySelector('[data-mode="hard"]').classList.toggle("on", !soft);
    row.querySelector('[data-mode="soft"]').classList.toggle("on", soft);
    row.classList.toggle("is-soft", soft);
    row.querySelector(".soft-fields").hidden = !soft;
    // Higher is better for > and >=, so the worst acceptable is below it.
    row.querySelector("[data-floor-label]").textContent =
      q("op").value.startsWith(">") ? "Accept down to" : "Accept up to";
  };
  Object.defineProperty(row, "soft", { get: () => soft });
  q("op").addEventListener("change", sync);
  for (const b of row.querySelectorAll("[data-mode]")) {
    b.addEventListener("click", () => {
      soft = b.dataset.mode === "soft";
      sync();
      row.dispatchEvent(new Event("input", { bubbles: true }));
    });
  }
  sync();
  row.querySelector("[data-remove]").addEventListener("click", () => {
    const editor = row.parentElement;
    row.remove();
    editor?.dispatchEvent(new Event("input", { bubbles: true }));
  });
  return row;
}

// conditionFromRow reads a row back into the stored condition shape. Any
// key added to the condition shape must be read here too, or saving from
// the editor drops it.
function conditionFromRow(row) {
  const q = (k) => row.querySelector(`[data-k="${k}"]`);
  const c = { field: q("field").value.trim(), op: q("op").value, value: q("value").value.trim() };
  if (row.soft) {
    c.soft = true;
    for (const k of ["floor", "best", "weight"]) {
      const v = q(k).value.trim();
      if (v !== "") c[k] = Number(v);
    }
  }
  return c;
}

// conditionText is a condition in words: "price at most $800,000".
function conditionText(c) {
  return `<b>${esc(c.field)}</b> ${esc(OP_LABELS[c.op] || c.op)} ${esc(fmtValue(c.field, c.value))}`;
}

function conditionChip(c) {
  if (!c.soft) return `<span class="chip">${conditionText(c)}</span>`;
  const range = `ok to ${fmtValue(c.field, c.floor)} · ideal ${fmtValue(c.field, c.best)}`;
  const weight = c.weight && c.weight !== 1 ? ` · counts ×${esc(c.weight)}` : "";
  return `<span class="chip chip-soft" title="Flexible: anything past ${esc(c.floor)} is rejected; ${esc(c.best)} or better earns full credit">${conditionText(c)} <span class="dim">· ${esc(range)}${weight}</span></span>`;
}

function checkGrid(items, selected, labelFn) {
  const grid = el(`<div class="check-grid"></div>`);
  for (const item of items) {
    const on = selected.includes(item.id);
    const label = el(`
      <label class="check-item ${on ? "on" : ""}">
        <input type="checkbox" value="${esc(item.id)}" ${on ? "checked" : ""}>
        <span class="truncate">${esc(labelFn(item))}</span>
      </label>`);
    const input = label.querySelector("input");
    input.addEventListener("change", () => label.classList.toggle("on", input.checked));
    grid.append(label);
  }
  return grid;
}

const checked = (grid) => [...grid.querySelectorAll("input:checked")].map((i) => i.value);

function previewRow(l) {
  const score = typeof l.score === "number" ? `<span class="score" title="${esc((l.parts || []).map((p) =>
    `${p.field} ${p.value ?? "missing"} (${p.score >= 0 ? "+" : ""}${p.score.toFixed(2)}${p.weight !== 1 ? ` ×${p.weight}` : ""})`).join(", "))}">score ${l.score >= 0 ? "+" : ""}${l.score.toFixed(2)}</span>` : "";
  return `
    <a class="preview-item" href="${esc(l.url || "#")}" target="_blank" rel="noopener">
      ${thumb(l.image)}
      <span class="preview-text">
        <span class="truncate"><b>${esc(l.title || "(untitled)")}</b></span>
        <span class="facts">${l.price > 0 ? `<span class="fact">${esc(money(l.price))}</span>` : ""}${dealChip(l.attrs)}${factChips(l.attrs)}</span>
      </span>
      ${score}
    </a>`;
}

// seekerPreview runs the editor's current settings through
// /api/seekout/preview on every change (debounced), so a search shows what
// it would have caught before it is saved.
function seekerPreview(read) {
  const box = el(`
    <section class="preview-panel" aria-live="polite">
      <div class="preview-head"><span class="spinner"></span><span class="small muted">Checking recent listings…</span></div>
      <div class="preview-list"></div>
    </section>`);
  const head = box.querySelector(".preview-head");
  const list = box.querySelector(".preview-list");
  // A one-line copy for the dialog footer, which stays in view while a
  // long condition list scrolls.
  const summary = el(`<button type="button" class="preview-summary" title="Show the matches"></button>`);
  summary.addEventListener("click", () => box.scrollIntoView({ behavior: "smooth", block: "center" }));
  let timer, seq = 0;
  const run = async () => {
    const mine = ++seq;
    const { kind, sources, conditions } = read();
    head.innerHTML = `<span class="spinner"></span><span class="small muted">Checking recent listings…</span>`;
    try {
      const res = await pb.send("/api/seekout/preview", {
        method: "POST",
        body: { kind, sources, conditions, days: 30, limit: 5 },
      });
      if (mine !== seq) return;
      const n = res.matched;
      summary.className = `preview-summary ${n ? "" : "zero"}`;
      summary.textContent = `${n.toLocaleString()} recent ${n === 1 ? "match" : "matches"}`;
      head.innerHTML = `
        <span class="preview-count ${n ? "" : "zero"}">${n.toLocaleString()}</span>
        <span class="small">${n === 1 ? "listing" : "listings"} from the last ${res.days} days would have matched
        <span class="dim">(of ${res.scanned.toLocaleString()}${res.capped ? ", newest only" : ""})</span></span>`;
      list.innerHTML = n
        ? res.listings.map(previewRow).join("") + (n > res.listings.length ? `<p class="small dim">and ${(n - res.listings.length).toLocaleString()} more</p>` : "")
        : `<p class="small muted">Nothing recent fits. That's fine if you're waiting for something rare; otherwise loosen a condition or make it flexible.</p>`;
    } catch (err) {
      if (mine !== seq) return;
      summary.className = "preview-summary warn";
      summary.textContent = "check conditions";
      head.innerHTML = `<span class="small warn-text">${esc(apiError(err))}</span>`;
      list.replaceChildren();
    }
  };
  return {
    box,
    summary,
    refresh: () => {
      clearTimeout(timer);
      timer = setTimeout(run, 350);
    },
    run,
  };
}

// "Good deals only" is ordinary conditions on attrs.deal: a minimum % under
// typical, an optional maximum, and confidence. The editor shows them as one
// switch; splitDeal pulls them out of a stored list and dealConditions puts
// them back.
const DEAL_DEFAULT = 15;
function splitDeal(conds) {
  const pctCond = conds.find((c) => c.field === "deal.pct_below" && c.op === ">=" && !c.soft);
  if (!pctCond) return { pct: null, max: null, rest: conds };
  const maxCond = conds.find((c) => c.field === "deal.pct_below" && c.op === "<=" && !c.soft);
  const rest = conds.filter((c) => c !== pctCond && c !== maxCond && !(c.field === "deal.confidence" && c.op === "in"));
  return { pct: Number(pctCond.value), max: maxCond ? Number(maxCond.value) : null, rest };
}
const dealConditions = (pct, max) => [
  { field: "deal.pct_below", op: ">=", value: pct },
  ...(max > 0 ? [{ field: "deal.pct_below", op: "<=", value: max }] : []),
  { field: "deal.confidence", op: "in", value: "high,medium" },
];

function seekerModal(seeker) {
  const editing = Boolean(seeker);
  const { pct: dealPct, max: dealMax, rest: conds } = splitDeal(Array.isArray(seeker?.conditions) ? seeker.conditions : []);

  const body = el(`
    <div class="stack">
      <div class="field">
        <label>Name</label>
        <input name="name" type="text" required placeholder="e.g. Lehi home under $800k" value="${esc(seeker?.name || "")}">
      </div>
      <div class="field">
        <label>Listing type</label>
        <select name="kind">
          <option value="any" ${!seeker || seeker.kind === "any" ? "selected" : ""}>Any</option>
          ${state.plugins.kinds.map((k) => `<option value="${esc(k)}" ${seeker?.kind === k ? "selected" : ""}>${esc(k[0].toUpperCase() + k.slice(1))}</option>`).join("")}
        </select>
      </div>
      <div class="field">
        <label>Conditions</label>
        <p class="hint">Must-haves all have to hold. Flexible ones trade off: falling short on one is fine if another more than makes up for it. Leave empty to match everything.</p>
        <div class="cond-editor"></div>
        <button type="button" class="btn btn-sm" data-add-cond>+ Add condition</button>
        <datalist id="field-suggestions">
          ${[...LISTING_FIELDS, ...state.attrKeys].map((f) => `<option value="${esc(f)}">`).join("")}
        </datalist>
      </div>
      <div class="field deal-field">
        <label class="row switch-row">
          <span class="switch"><input type="checkbox" data-deal ${dealPct !== null ? "checked" : ""}><span class="track"></span></span>
          <span>Good deals only</span>
        </label>
        <div class="deal-opts small" ${dealPct !== null ? "" : "hidden"}>
          <label>From <input type="number" data-deal-pct min="1" max="90" step="1" value="${esc(dealPct ?? DEAL_DEFAULT)}" aria-label="At least this % under typical"></label>
          <label>to <input type="number" data-deal-max min="1" max="99" step="1" placeholder="any" value="${esc(dealMax ?? "")}" aria-label="At most this % under typical (blank for no limit)"></label>
          <span>% under the typical price</span>
        </div>
        <p class="hint">Typical is what this kind of item goes for in recent listings, checked against web prices every few days. It is per unit (per GB for memory), so different sizes compare. Listings that can't be priced, like whole systems on a parts search, won't match.</p>
      </div>
      <div data-preview></div>
      <div class="field" data-alerters>
        <label>Notify me with</label>
      </div>
      <details class="field more-options" ${seeker?.sources?.length ? "open" : ""}>
        <summary>Only from certain sources <span class="hint">${seeker?.sources?.length ? "" : "currently all sources"}</span></summary>
      </details>
    </div>`);

  const editor = body.querySelector(".cond-editor");
  for (const c of conds) editor.append(conditionRow(c));
  if (!conds.length) editor.append(conditionRow());
  body.querySelector("[data-add-cond]").addEventListener("click", () => {
    const row = conditionRow();
    editor.append(row);
    row.querySelector("input").focus();
  });

  const sourcesGrid = checkGrid(state.sources, seeker?.sources || [], (s) => s.name);
  body.querySelector(".more-options").append(sourcesGrid);

  const alerterField = body.querySelector("[data-alerters]");
  // A new search starts wired to the user's only notification.
  const preselect = seeker ? seeker.alerters || [] : state.alerters.length === 1 ? [state.alerters[0].id] : [];
  const alertersGrid = checkGrid(state.alerters, preselect, (a) => a.name);
  if (state.alerters.length) {
    alerterField.append(alertersGrid);
  } else {
    const hint = el(`<p class="hint">You have no notifications yet, so matches will only show in the app. <a href="#/alerters">Set one up</a> and come back.</p>`);
    alerterField.append(hint);
  }

  const enabled = switchField("Active: check new listings against this search", seeker ? seeker.enabled : true);
  body.append(enabled);

  const dealOn = body.querySelector("[data-deal]");
  const dealPctInput = body.querySelector("[data-deal-pct]");
  const dealMaxInput = body.querySelector("[data-deal-max]");
  dealOn.addEventListener("change", () => {
    body.querySelector(".deal-opts").hidden = !dealOn.checked;
  });
  const read = () => {
    const conditions = [...editor.querySelectorAll(".cond-row")].map(conditionFromRow).filter((c) => c.field);
    if (dealOn.checked) conditions.push(...dealConditions(Number(dealPctInput.value) || DEAL_DEFAULT, Number(dealMaxInput.value)));
    return {
      kind: body.querySelector('[name="kind"]').value,
      sources: checked(sourcesGrid),
      conditions,
    };
  };
  const preview = seekerPreview(read);
  body.querySelector("[data-preview]").replaceWith(preview.box);
  body.addEventListener("input", (e) => {
    if (!e.target.closest?.("[data-alerters]")) preview.refresh();
  });
  body.addEventListener("change", (e) => {
    if (!e.target.closest?.("[data-alerters]")) preview.refresh();
  });
  preview.run();

  openModal({
    title: editing ? "Edit search" : "New search",
    body,
    wide: true,
    footerExtra: preview.summary,
    submitLabel: editing ? "Save" : "Create search",
    onSubmit: async () => {
      const data = {
        user: pb.authStore.record.id,
        name: body.querySelector('[name="name"]').value.trim(),
        alerters: checked(alertersGrid),
        enabled: enabled.querySelector("input").checked,
        ...read(),
      };
      if (editing) await pb.collection("seekers").update(seeker.id, data);
      else await pb.collection("seekers").create(data);
      toast(editing ? "Search saved" : "Search created. New listings that fit will show up in Matches.", "ok");
      await loadAll();
      route();
    },
  });
}

async function viewSeekers() {
  const add = button("+ New search", "btn-primary", () => seekerModal(null));
  setHead("Searches", "What you're looking for. New listings are checked against each active search.", [add]);

  const box = el(`<div class="view-inner"></div>`);
  if (!state.seekers.length) {
    box.append(emptyState(ICON_SEARCH, "No searches yet",
      "A search describes what you want: the must-haves, and the nice-to-haves that can trade off. You'll see what it would have caught while you build it.",
      button("+ New search", "btn-primary", () => seekerModal(null))));
    $("view").replaceChildren(box);
    return;
  }

  // Match counts per search: one tiny request each (searches are few).
  const counts = await Promise.all(state.seekers.map((s) =>
    pb.collection("alerts").getList(1, 1, { filter: pb.filter("seeker = {:s}", { s: s.id }), sort: "-created", fields: "created" })
      .catch(() => ({ totalItems: 0, items: [] }))));

  state.seekers.forEach((s, i) => {
    const conds = Array.isArray(s.conditions) ? s.conditions : [];
    const hard = conds.filter((c) => !c.soft);
    const soft = conds.filter((c) => c.soft);
    const { totalItems, items } = counts[i];
    const notify = s.alerters?.length
      ? `<span>Notifies ${s.alerters.map(alerterName).map(esc).join(", ")}</span>`
      : `<span class="warn-text" title="Matches only show in the app">No notification set</span>`;
    const card = el(`
      <article class="card seeker-card ${s.enabled ? "" : "is-paused"}">
        <div class="item">
          <div class="item-head">
            <div class="item-title">
              <span>${esc(s.name)}</span>
              ${s.kind && s.kind !== "any" ? `<span class="badge">${esc(s.kind)}</span>` : ""}
              ${s.enabled ? "" : `<span class="badge badge-warn">paused</span>`}
            </div>
            <div class="item-meta">
              ${notify}
              <a href="#/alerts" data-matches>${totalItems ? `${plural(totalItems, "match")} · last ${esc(timeAgo(items[0]?.created))}` : "no matches yet"}</a>
              ${s.sources?.length ? `<span>${esc(s.sources.map(sourceName).join(", "))}</span>` : ""}
            </div>
          </div>
          <div class="item-actions">
            <label class="switch" title="${s.enabled ? "Pause" : "Resume"} this search">
              <input type="checkbox" data-toggle ${s.enabled ? "checked" : ""} aria-label="Active"><span class="track"></span>
            </label>
            <button class="btn btn-sm" data-edit>Edit</button>
            <button class="btn btn-sm btn-ghost btn-danger" data-del>Delete</button>
          </div>
          <div class="item-note cond-groups">
            ${hard.length ? `<div class="cond-group"><span class="cond-label">Must have</span><div class="cond-list">${hard.map(conditionChip).join("")}</div></div>` : ""}
            ${soft.length ? `<div class="cond-group"><span class="cond-label">Flexible</span><div class="cond-list">${soft.map(conditionChip).join("")}</div></div>` : ""}
            ${conds.length ? "" : `<span class="chip">matches every new listing</span>`}
          </div>
        </div>
      </article>`);

    card.querySelector("[data-matches]").addEventListener("click", () => (state.alertSeeker = s.id));
    card.querySelector("[data-edit]").addEventListener("click", () => seekerModal(s));
    const toggle = card.querySelector("[data-toggle]");
    toggle.addEventListener("change", async () => {
      try {
        await pb.collection("seekers").update(s.id, { enabled: toggle.checked });
        toast(toggle.checked ? `"${s.name}" is active` : `"${s.name}" is paused`, "ok");
        await loadAll();
        route();
      } catch (err) {
        toggle.checked = !toggle.checked;
        toast(apiError(err), "err");
      }
    });
    card.querySelector("[data-del]").addEventListener("click", async () => {
      if (!(await confirmDialog("Delete search", `Delete "${s.name}"? Its matches go with it.`))) return;
      try {
        await pb.collection("seekers").delete(s.id);
        toast("Search deleted", "ok");
        await loadAll();
        route();
      } catch (err) {
        toast(apiError(err), "err");
      }
    });
    box.append(card);
  });
  $("view").replaceChildren(box);
}

/* ---- sources ---- */

function sourceModal(source) {
  const editing = Boolean(source);
  const picker = typePicker("sources", source);
  const body = el(`
    <div class="stack">
      <div class="field">
        <label>Name</label>
        <input name="name" type="text" required placeholder="e.g. Utah homes" value="${esc(source?.name || "")}">
      </div>
      <div class="field" data-type>
        <label>Source type</label>
      </div>
      <div data-config></div>
      <div class="row row-wrap">
        <div class="field grow">
          <label>Lists</label>
          <select name="kind">
            ${state.plugins.kinds.map((k) => `<option value="${esc(k)}" ${source?.kind === k ? "selected" : ""}>${esc(k)}</option>`).join("")}
          </select>
        </div>
        <div class="field grow">
          <label>Check every <span class="hint">minutes</span></label>
          <input name="interval" type="number" min="1" required value="${esc(source?.interval_minutes || 30)}">
        </div>
      </div>
      <p class="hint">The first check of a new source stores what's already listed without alerting; matches start with listings that appear after that.</p>
    </div>`);
  body.querySelector("[data-type]").append(picker.select);
  body.querySelector("[data-config]").replaceWith(picker.section);

  const enabled = switchField("Active: check on schedule", source ? source.enabled : true);
  body.append(enabled);

  openModal({
    title: editing ? "Edit source" : "New source",
    body,
    submitLabel: editing ? "Save changes" : "Create source",
    onSubmit: async () => {
      const data = {
        name: body.querySelector('[name="name"]').value.trim(),
        type: picker.select.value,
        kind: body.querySelector('[name="kind"]').value,
        config: picker.read(),
        interval_minutes: Number(body.querySelector('[name="interval"]').value),
        enabled: enabled.querySelector("input").checked,
      };
      if (editing) await pb.collection("sources").update(source.id, data);
      else await pb.collection("sources").create(data);
      toast(editing ? "Source saved" : "Source created", "ok");
      await loadAll();
      route();
    },
  });
}

function sourceHealth(s) {
  if (!s.enabled) return `<span class="health"><span class="dot dot-off"></span>paused</span>`;
  if (s.last_error) return `<span class="health err"><span class="dot dot-err"></span>failing</span>`;
  if (!s.last_polled) return `<span class="health"><span class="dot dot-off"></span>not checked yet</span>`;
  return `<span class="health ok"><span class="dot dot-ok"></span>healthy</span>`;
}

async function viewSources() {
  const admin = isAdmin();
  const add = button("+ New source", "btn-primary", () => sourceModal(null));
  setHead("Sources", admin
    ? "The sites seekout checks for new listings, shared by everyone"
    : "The sites seekout checks for new listings. They're shared, and an admin manages them.",
  admin ? [add] : []);

  const box = el(`<div class="view-inner"></div>`);
  if (!state.sources.length) {
    box.append(emptyState(ICON_FEED, "No sources yet", admin
      ? "Add an RSS or Atom feed, or the Utah MLS: anything that lists things."
      : "Sources are shared, and an admin adds them.", admin ? button("+ New source", "btn-primary", () => sourceModal(null)) : undefined));
    $("view").replaceChildren(box);
    return;
  }

  for (const s of state.sources) {
    const info = pluginInfo("sources", s.type);
    const card = el(`
      <article class="card">
        <div class="item">
          <div class="item-head">
            <div class="item-title">
              <span>${esc(s.name)}</span>
              ${sourceHealth(s)}
            </div>
            <div class="item-meta">
              <span>${esc(info?.label || s.type)} · ${esc(s.kind)}</span>
              <span class="mono truncate meta-config">${esc(configSummary(info, s.config))}</span>
              <span>${esc(everyLabel(s.interval_minutes))}</span>
              <span title="${esc(absTime(s.last_polled))}">checked ${esc(timeAgo(s.last_polled))}</span>
              ${s.seeded ? "" : `<span class="badge badge-accent" title="The first check stores existing listings without alerting">first check pending</span>`}
            </div>
          </div>
          <div class="item-actions">
            <span class="small muted" data-status>${esc(state.pollResults[s.id] || "")}</span>
            <button class="btn btn-sm" data-poll>Check now</button>
            ${admin ? `<button class="btn btn-sm" data-edit>Edit</button>
            <button class="btn btn-sm btn-ghost btn-danger" data-del>Delete</button>` : ""}
          </div>
          ${s.last_error ? `<div class="item-note error-note">
            <svg viewBox="0 0 24 24" width="15" height="15" style="flex:none"><path d="M12 8v5m0 3.5v.5" stroke="currentColor" stroke-width="2" stroke-linecap="round"/><circle cx="12" cy="12" r="9" stroke="currentColor" stroke-width="1.6" fill="none"/></svg>
            <span>${esc(s.last_error)}</span></div>` : ""}
        </div>
      </article>`);

    const status = card.querySelector("[data-status]");
    const pollBtn = card.querySelector("[data-poll]");
    pollBtn.addEventListener("click", async () => {
      pollBtn.disabled = true;
      status.replaceChildren(el(`<span class="row"><span class="spinner"></span> checking…</span>`));
      try {
        const res = await pb.send(`/api/seekout/sources/${s.id}/poll`, { method: "POST" });
        state.pollResults[s.id] = res.seeded
          ? `stored ${res.new} existing`
          : `${res.new} new · ${res.matched} matched`;
        toast(res.seeded
          ? `${s.name}: stored ${plural(res.new, "existing listing")}; matches start with the next new one`
          : `${s.name}: ${res.new} new, ${res.matched} matched`, "ok");
      } catch (err) {
        toast(apiError(err), "err");
      }
      await loadAll();
      if (currentRoute() === "sources") route();
    });

    card.querySelector("[data-edit]")?.addEventListener("click", () => sourceModal(s));
    card.querySelector("[data-del]")?.addEventListener("click", async () => {
      if (!(await confirmDialog("Delete source", `Delete "${s.name}"? Its listings and everyone's matches from it go with it.`))) return;
      try {
        await pb.collection("sources").delete(s.id);
        toast("Source deleted", "ok");
        await loadAll();
        route();
      } catch (err) {
        toast(apiError(err), "err");
      }
    });
    box.append(card);
  }
  $("view").replaceChildren(box);
}

/* ---- listings ---- */

function attrValue(v) {
  if (v === true) return "yes";
  if (v === false) return "no";
  if (Array.isArray(v)) return v.join(", ") || "—";
  if (v && typeof v === "object") return JSON.stringify(v);
  return String(v);
}

function listingCard(l) {
  const attrs = l.attrs || {};
  const keys = Object.keys(attrs).sort();
  const card = el(`
    <article class="card listing-card">
      ${thumb(l.image, false)}
      <div class="listing-main">
        <div class="listing-top">
          <a class="item-title" ${l.url ? `href="${esc(l.url)}" target="_blank" rel="noopener"` : ""}>${esc(l.title || "(untitled)")}</a>
          ${l.price > 0 ? `<span class="price tnum">${esc(money(l.price))}</span>` : ""}
        </div>
        <div class="facts">${statusBadge(attrs)}${dealChip(attrs)}${factChips(attrs)}</div>
        <div class="item-meta">
          <span>${esc(l.expand?.source?.name || sourceName(l.source))}</span>
          <span title="${esc(absTime(l.created))}">found ${esc(timeAgo(l.created))}</span>
          ${keys.length ? `<button type="button" class="link-btn" data-details aria-expanded="false">All fields</button>` : ""}
        </div>
        <dl class="attr-grid" hidden>
          ${keys.map((k) => `<div><dt class="mono">${esc(k)}</dt><dd>${esc(attrValue(attrs[k]))}</dd></div>`).join("")}
        </dl>
      </div>
    </article>`);
  const toggle = card.querySelector("[data-details]");
  toggle?.addEventListener("click", () => {
    const grid = card.querySelector(".attr-grid");
    grid.hidden = !grid.hidden;
    toggle.setAttribute("aria-expanded", String(!grid.hidden));
    toggle.textContent = grid.hidden ? "All fields" : "Hide fields";
  });
  return card;
}

function listingsFilter() {
  const parts = [];
  if (state.listingFilter) parts.push(pb.filter("source = {:s}", { s: state.listingFilter }));
  if (state.listingQuery) parts.push(pb.filter("title ~ {:q}", { q: state.listingQuery }));
  return parts.join(" && ");
}

const listingsPage = (page) => pb.collection("listings").getList(page, PAGE, {
  sort: "-created",
  expand: "source",
  filter: listingsFilter(),
});

async function viewListings() {
  setHead("Listings", "Everything the sources have found. Field names shown here are what search conditions can use.");

  const toolbar = el(`
    <div class="toolbar">
      <input type="search" placeholder="Search titles, e.g. Lehi" aria-label="Search titles" value="${esc(state.listingQuery)}">
      <select class="select-auto" aria-label="Source">
        <option value="">All sources</option>
        ${state.sources.map((s) => `<option value="${esc(s.id)}" ${state.listingFilter === s.id ? "selected" : ""}>${esc(s.name)}</option>`).join("")}
      </select>
    </div>`);
  const results = el(`<div class="stack-sm"></div>`);
  const box = el(`<div class="view-inner"></div>`);
  box.append(toolbar, results);
  $("view").replaceChildren(box);

  const render = async () => {
    results.replaceChildren(...skeletons(3).children);
    const res = await listingsPage(1);
    if (!res.items.length) {
      results.replaceChildren(emptyState(ICON_LIST, state.listingQuery || state.listingFilter ? "No listings match" : "Nothing found yet",
        state.listingQuery || state.listingFilter
          ? "Try a different word or source."
          : "Listings appear here after a source is checked."));
      return;
    }
    results.replaceChildren(el(`<p class="small dim">${res.totalItems.toLocaleString()} ${res.totalItems === 1 ? "listing" : "listings"}${state.listingQuery || state.listingFilter ? " match" : " stored"}</p>`));
    for (const l of res.items) results.append(listingCard(l));
    loadMore(results, listingsPage, listingCard, res.page, res.totalPages);
  };

  let timer;
  toolbar.querySelector("input").addEventListener("input", (e) => {
    clearTimeout(timer);
    timer = setTimeout(() => {
      state.listingQuery = e.target.value.trim();
      render().catch((err) => toast(apiError(err), "err"));
    }, 300);
  });
  toolbar.querySelector("select").addEventListener("change", (e) => {
    state.listingFilter = e.target.value;
    render().catch((err) => toast(apiError(err), "err"));
  });
  await render();
}

/* ---- prices (price-tracked items) ---- */

function priceStats(it) {
  const parts = [];
  const e = it.estimate;
  if (e) parts.push(`<span class="fact" title="${esc(`${e.basis}, ${e.confidence} confidence`)}">typical <b>${esc(perUnit(e.typical, it.unit))}</b></span>`);
  parts.push(`<span class="fact">${esc(plural(it.comps, "listing"))} in 30 days${it.comps ? ` · median ${esc(perUnit(it.comps_median, it.unit))}` : ""}</span>`);
  const a = it.anchor;
  if (a?.status === "ok") {
    parts.push(`<span class="fact" title="${esc(`web research ${absTime(a.created)}: ${a.n_used} used and ${a.n_new} new prices`)}">web ${esc(perUnit(a.typical, it.unit))}
      <span class="dim">(${esc(perUnit(a.low, it.unit))}–${esc(perUnit(a.high, it.unit))}${a.new ? ` · new ${esc(perUnit(a.new, it.unit))}` : ""})</span></span>`);
  } else if (a) {
    parts.push(`<span class="fact dim">web research: ${esc(a.status === "no_data" ? "no prices found" : "failed")}</span>`);
  } else {
    parts.push(`<span class="fact dim">not researched yet</span>`);
  }
  return parts.join("");
}

function priceDetail(d) {
  const unit = d.unit;
  const obs = d.observations.map((o) => `
    <li class="price-row ${o.repost ? "dim" : ""}">
      <a class="truncate" ${o.url ? `href="${esc(o.url)}" target="_blank" rel="noopener"` : ""}>${esc(o.label || o.title)}</a>
      <span class="tnum">${esc(money(o.total_price))}${o.quantity > 1 ? ` for ${o.quantity}` : ""}${unit !== "each" ? ` · ${o.quantity > 1 ? `${o.quantity}×` : ""}${o.amount} ${esc(unit)}` : ""}</span>
      <b class="tnum">${esc(perUnit(o.unit_price, unit))}</b>
      <span class="small dim" title="${esc(absTime(o.observed_at))}">${esc(timeAgo(o.observed_at))}${o.repost ? " · repost" : ""}</span>
    </li>`).join("");
  const ref = d.refs.find((r) => r.status === "ok");
  const cites = ref ? (ref.citations || []).map((c) => `
    <li class="price-row">
      <a class="truncate" href="${esc(c.url)}" target="_blank" rel="noopener" title="${esc(c.quote)}">${esc(c.product || c.title || c.url)}</a>
      <span class="tnum">${esc(money(c.price))}${unit !== "each" ? ` · ${c.amount} ${esc(unit)}` : ""}</span>
      <b class="tnum">${esc(perUnit(c.unit_price, unit))}</b>
      <span class="small dim">${esc(c.condition)}</span>
    </li>`).join("") : "";
  return `
    ${d.description ? `<p class="small muted">${esc(d.description)}</p>` : ""}
    <h4 class="price-h">Listings</h4>
    ${obs ? `<ul class="price-list">${obs}</ul>` : `<p class="small muted">None in the last 90 days.</p>`}
    <h4 class="price-h">Web prices${ref ? ` <span class="small dim">${esc(timeAgo(ref.created))}${ref.rejected ? ` · ${ref.rejected} unverified dropped` : ""}</span>` : ""}</h4>
    ${cites ? `<ul class="price-list">${cites}</ul>` : `<p class="small muted">${d.refs.length ? "The last research found no verifiable prices." : "Not researched yet; research runs daily for items due."}</p>`}`;
}

async function viewPrices() {
  setHead("Prices", "What each kind of item goes for: recent listings, anchored by web prices. Searches with \"Good deals only\" compare against these.");
  const box = el(`<div class="view-inner stack-sm"></div>`);
  box.append(...skeletons(3).children);
  $("view").replaceChildren(box);
  const res = await pb.send("/api/seekout/items", {});
  box.replaceChildren();
  if (!res.items.length) {
    box.append(emptyState(ICON_LIST, res.enabled ? "Nothing priced yet" : "Price tracking is off",
      res.enabled
        ? "Listings your searches want are priced as they arrive. Items show up here after the first one."
        : "The server has no LLM configured, so listings aren't being priced."));
    return;
  }
  for (const it of res.items) {
    const card = el(`
      <article class="card">
        <div class="item">
          <div class="item-head">
            <div class="item-title"><span>${esc(it.name)}</span></div>
            <div class="item-meta">
              <span>per ${esc(it.unit === "each" ? "item" : it.unit)}${it.category ? ` · ${esc(it.category)}` : ""}</span>
              <span title="${esc(absTime(it.last_observed))}">last seen ${esc(timeAgo(it.last_observed))}</span>
            </div>
            <div class="facts price-facts">${priceStats(it)}</div>
          </div>
          <div class="item-actions"><button class="btn btn-sm" data-more aria-expanded="false">Details</button></div>
          <div class="price-detail" hidden></div>
        </div>
      </article>`);
    const more = card.querySelector("[data-more]");
    const detail = card.querySelector(".price-detail");
    more.addEventListener("click", async () => {
      if (!detail.hidden) {
        detail.hidden = true;
        more.setAttribute("aria-expanded", "false");
        more.textContent = "Details";
        return;
      }
      if (!detail.dataset.loaded) {
        detail.innerHTML = `<span class="spinner"></span>`;
        detail.hidden = false;
        try {
          detail.innerHTML = priceDetail(await pb.send(`/api/seekout/items/${it.id}`, {}));
          detail.dataset.loaded = "1";
        } catch (err) {
          detail.innerHTML = `<div class="error-note">${esc(apiError(err))}</div>`;
        }
      }
      detail.hidden = false;
      more.setAttribute("aria-expanded", "true");
      more.textContent = "Hide";
    });
    box.append(card);
  }
}

/* ---- notifications (alerters) ---- */

function alerterModal(alerter) {
  const editing = Boolean(alerter);
  const picker = typePicker("alerters", alerter);
  const body = el(`
    <div class="stack">
      <div class="field">
        <label>Name</label>
        <input name="name" type="text" required placeholder="e.g. My phone" value="${esc(alerter?.name || "")}">
      </div>
      <div class="field" data-type>
        <label>Send with</label>
      </div>
    </div>`);
  body.querySelector("[data-type]").append(picker.select);
  body.append(picker.section);

  const enabled = switchField("Active: send matches here", alerter ? alerter.enabled : true);
  body.append(enabled);

  openModal({
    title: editing ? "Edit notification" : "New notification",
    body,
    submitLabel: editing ? "Save changes" : "Create notification",
    onSubmit: async () => {
      const data = {
        user: pb.authStore.record.id,
        name: body.querySelector('[name="name"]').value.trim(),
        type: picker.select.value,
        config: picker.read(),
        enabled: enabled.querySelector("input").checked,
      };
      if (editing) await pb.collection("alerters").update(alerter.id, data);
      else await pb.collection("alerters").create(data);
      toast(editing ? "Notification saved" : "Notification created. Use \"Send test\" to check it arrives.", "ok");
      await loadAll();
      route();
    },
  });
}

async function viewAlerters() {
  const add = button("+ New notification", "btn-primary", () => alerterModal(null));
  setHead("Notifications", "Where your matches get sent", [add]);

  const box = el(`<div class="view-inner"></div>`);
  if (!state.alerters.length) {
    box.append(emptyState(ICON_SEND, "No notifications yet",
      "Get a push on your phone with ntfy, or post matches to Slack, Discord or anything else with a webhook.",
      button("+ New notification", "btn-primary", () => alerterModal(null))));
    $("view").replaceChildren(box);
    return;
  }

  for (const a of state.alerters) {
    const info = pluginInfo("alerters", a.type);
    const usedBy = state.seekers.filter((s) => s.alerters?.includes(a.id));
    const card = el(`
      <article class="card">
        <div class="item">
          <div class="item-head">
            <div class="item-title">
              <span>${esc(a.name)}</span>
              <span class="badge badge-accent">${esc(info?.label || a.type)}</span>
              ${a.enabled ? "" : `<span class="badge badge-warn">paused</span>`}
            </div>
            <div class="item-meta">
              <span class="mono truncate meta-config">${esc(configSummary(info, a.config))}</span>
              <span>${usedBy.length ? `Used by ${esc(usedBy.map((s) => s.name).join(", "))}` : `<span class="warn-text">Not used by any search</span>`}</span>
            </div>
          </div>
          <div class="item-actions">
            <span class="small muted" data-status></span>
            <button class="btn btn-sm" data-test>Send test</button>
            <button class="btn btn-sm" data-edit>Edit</button>
            <button class="btn btn-sm btn-ghost btn-danger" data-del>Delete</button>
          </div>
        </div>
      </article>`);

    const status = card.querySelector("[data-status]");
    const testBtn = card.querySelector("[data-test]");
    testBtn.addEventListener("click", async () => {
      testBtn.disabled = true;
      status.replaceChildren(el(`<span class="row"><span class="spinner"></span> sending…</span>`));
      try {
        await pb.send(`/api/seekout/alerters/${a.id}/test`, { method: "POST" });
        status.replaceChildren(el(`<span class="badge badge-ok badge-dot">delivered</span>`));
        toast(`Test sent to ${a.name}`, "ok");
      } catch (err) {
        status.replaceChildren(el(`<span class="badge badge-err badge-dot">failed</span>`));
        toast(apiError(err), "err");
      } finally {
        testBtn.disabled = false;
      }
    });

    card.querySelector("[data-edit]").addEventListener("click", () => alerterModal(a));
    card.querySelector("[data-del]").addEventListener("click", async () => {
      const msg = usedBy.length
        ? `Delete "${a.name}"? ${usedBy.map((s) => s.name).join(", ")} will stop notifying you.`
        : `Delete "${a.name}"?`;
      if (!(await confirmDialog("Delete notification", msg))) return;
      try {
        await pb.collection("alerters").delete(a.id);
        toast("Notification deleted", "ok");
        await loadAll();
        route();
      } catch (err) {
        toast(apiError(err), "err");
      }
    });
    box.append(card);
  }
  $("view").replaceChildren(box);
}

/* ---- invites (admins) ---- */

const inviteLink = (code) => `${location.origin}/?invite=${encodeURIComponent(code)}`;

async function copyText(text, what) {
  try {
    await navigator.clipboard.writeText(text);
    toast(`${what} copied`, "ok");
  } catch {
    toast(text);
  }
}

function inviteModal() {
  const body = el(`
    <div class="stack">
      <div class="field">
        <label>Note <span class="hint">optional, e.g. who it's for</span></label>
        <input name="note" type="text" placeholder="for Sam">
      </div>
      <p class="hint">Each invite works for exactly one signup. Its link is copied when you create it.</p>
    </div>`);
  openModal({
    title: "New invite",
    body,
    submitLabel: "Create and copy link",
    onSubmit: async () => {
      const inv = await pb.collection("invites").create({ note: body.querySelector('[name="note"]').value.trim() });
      await copyText(inviteLink(inv.code), "Invite link");
      route();
    },
  });
}

async function viewInvites() {
  const add = button("+ New invite", "btn-primary", inviteModal);
  setHead("Invites", "Signup is invite-only. Each code works once.", [add]);

  const invites = await pb.collection("invites").getFullList({ sort: "-created" });
  const box = el(`<div class="view-inner"></div>`);
  if (!invites.length) {
    box.append(emptyState(ICON_MAIL, "No invites yet", "Create one and share its link.", button("+ New invite", "btn-primary", inviteModal)));
    $("view").replaceChildren(box);
    return;
  }
  for (const inv of invites) {
    const used = Boolean(inv.used_by);
    const card = el(`
      <article class="card">
        <div class="item">
          <div class="item-head">
            <div class="item-title">
              <span class="mono">${esc(inv.code)}</span>
              ${used ? `<span class="badge">used</span>` : `<span class="badge badge-ok">unused</span>`}
            </div>
            <div class="item-meta">
              ${inv.note ? `<span>${esc(inv.note)}</span>` : ""}
              <span title="${esc(absTime(inv.created))}">created ${esc(timeAgo(inv.created))}</span>
              ${used ? `<span title="${esc(absTime(inv.used_at))}">used ${esc(timeAgo(inv.used_at))}</span>` : ""}
            </div>
          </div>
          <div class="item-actions">
            ${used ? "" : `<button class="btn btn-sm" data-copy>Copy link</button>`}
            <button class="btn btn-sm btn-ghost btn-danger" data-del>Delete</button>
          </div>
        </div>
      </article>`);
    card.querySelector("[data-copy]")?.addEventListener("click", () => copyText(inviteLink(inv.code), "Invite link"));
    card.querySelector("[data-del]").addEventListener("click", async () => {
      if (!(await confirmDialog("Delete invite", used
        ? "Remove this used invite from the list? The account it created stays."
        : "Delete this invite? Its code stops working."))) return;
      try {
        await pb.collection("invites").delete(inv.id);
        toast("Invite deleted", "ok");
        route();
      } catch (err) {
        toast(apiError(err), "err");
      }
    });
    box.append(card);
  }
  $("view").replaceChildren(box);
}

/* ---------------------------------------------------------------- router -- */

const ROUTES = {
  alerts: viewAlerts,
  seekers: viewSeekers,
  sources: viewSources,
  listings: viewListings,
  prices: viewPrices,
  alerters: viewAlerters,
  invites: viewInvites,
};

function currentRoute() {
  const name = location.hash.replace(/^#\/?/, "") || "alerts";
  if (name === "invites" && !isAdmin()) return "alerts";
  return ROUTES[name] ? name : "alerts";
}

function renderNavCount() {
  const badge = $("nav-alerts-count");
  badge.hidden = state.unseen === 0;
  badge.textContent = state.unseen;
}

function closeNav() {
  $("sidebar").classList.remove("open");
  document.body.classList.remove("nav-open");
}

let routing = 0;
async function route() {
  const mine = ++routing;
  const name = currentRoute();
  for (const a of document.querySelectorAll("#nav a")) {
    a.classList.toggle("active", a.dataset.route === name);
  }
  closeNav();
  try {
    await ROUTES[name]();
  } catch (err) {
    if (mine !== routing) return;
    $("view").replaceChildren(el(`<div class="view-inner"><div class="error-note">${esc(apiError(err))}</div></div>`));
  }
}

window.addEventListener("hashchange", () => {
  // A dialog belongs to the page it was opened on.
  closeModals();
  if (currentRoute() !== "alerts") state.alertSeeker = "";
  route();
});
$("menu-toggle").addEventListener("click", () => {
  $("sidebar").classList.toggle("open");
  document.body.classList.toggle("nav-open", $("sidebar").classList.contains("open"));
});
// tapping the page behind the mobile drawer dismisses it
document.querySelector(".main").addEventListener("click", (e) => {
  if (!$("sidebar").classList.contains("open") || e.target.closest("#menu-toggle")) return;
  closeNav();
});
$("signout").addEventListener("click", () => {
  pb.authStore.clear();
  location.href = "/";
});

/* -------------------------------------------------------------- realtime -- */

async function subscribeAlerts() {
  await pb.collection("alerts").subscribe("*", async (e) => {
    if (e.action !== "create") return;
    let record = e.record;
    try {
      record = await pb.collection("alerts").getOne(e.record.id, { expand: "seeker,listing" });
    } catch { /* fall back to the unexpanded record */ }

    const list = $("alert-list");
    if (currentRoute() === "alerts" && (!state.alertSeeker || state.alertSeeker === record.seeker)) {
      if (list) {
        const card = alertCard(record);
        card.classList.add("alert-new");
        list.prepend(card);
        const stat = $("stat-alerts");
        if (stat) stat.textContent = Number(stat.textContent) + 1;
      } else if (!$("modal-host").children.length) {
        route();
      }
    } else {
      state.unseen++;
      renderNavCount();
    }
    toast(`New match: ${record.title || "listing"}`, "ok");
  });
}

/* ------------------------------------------------------------------ boot -- */

(async () => {
  if (!pb.authStore.isValid) {
    location.replace("/");
    return;
  }
  try {
    await pb.collection("users").authRefresh();
  } catch {
    pb.authStore.clear();
    location.replace("/");
    return;
  }

  const email = pb.authStore.record?.email || "";
  $("account-email").textContent = email;
  $("account-avatar").textContent = (email[0] || "?").toUpperCase();
  if (isAdmin()) {
    $("account-role").textContent = "admin";
    $("nav-shared").append(el(`
      <a href="#/invites" data-route="invites">
        ${ICON_MAIL}
        <span>Invites</span>
      </a>`));
  }

  try {
    await loadAll();
  } catch (err) {
    toast(apiError(err), "err");
  }
  loadAttrKeys().catch(() => {});
  await route();
  subscribeAlerts().catch(() => toast("Live updates unavailable; refresh to see new matches", "err"));
})();
