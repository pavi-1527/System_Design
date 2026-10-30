// sd serve — single-page front end. Plain JS, no build step. Everything it shows
// comes from the JSON API in internal/web; all grading and gate logic stays in Go.
"use strict";

const app = document.getElementById("app");

// ---------------------------------------------------------------- utilities

function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k === "class") el.className = v;
    else if (k === "html") el.innerHTML = v;
    else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (k === "dataset") Object.assign(el.dataset, v);
    else if (k === "style" && typeof v === "object") Object.assign(el.style, v);
    else el.setAttribute(k, v === true ? "" : v);
  }
  for (const kid of kids.flat(Infinity)) {
    if (kid == null || kid === false) continue;
    el.append(kid instanceof Node ? kid : document.createTextNode(String(kid)));
  }
  return el;
}

function esc(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

async function api(method, path, body) {
  const opt = { method, headers: {} };
  if (body !== undefined) {
    opt.headers["Content-Type"] = "application/json";
    opt.body = JSON.stringify(body);
  }
  const res = await fetch(path, opt);
  let data = null;
  try { data = await res.json(); } catch (e) { /* empty body */ }
  if (!res.ok) throw new Error((data && data.error) || `${res.status} ${res.statusText}`);
  return data;
}

const pct = (x) => `${Math.round((x || 0) * 100)}%`;
const fmtDate = (s) => (s ? new Date(s).toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" }) : "—");
const fmtDateTime = (s) => (s ? new Date(s).toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }) : "—");

function idLess(a, b) {
  if (a[0] !== b[0]) return a < b ? -1 : 1;
  if (a.length !== b.length) return a.length - b.length;
  return a < b ? -1 : a > b ? 1 : 0;
}

function lsGet(k) { try { return localStorage.getItem(k); } catch (e) { return null; } }
function lsSet(k, v) { try { v == null ? localStorage.removeItem(k) : localStorage.setItem(k, v); } catch (e) { /* private mode */ } }

function errorBox(err) {
  return h("div", { class: "error-box" }, String(err && err.message ? err.message : err));
}

function setTitle(t) { document.title = t ? `${t} · sd` : "System Design Course"; }

// ---------------------------------------------------------------- theme

const THEMES = ["auto", "light", "dark"];
function themePref() { const t = lsGet("sd-theme"); return THEMES.includes(t) ? t : "auto"; }
function effectiveTheme() {
  const t = themePref();
  if (t !== "auto") return t;
  return matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}
function applyTheme() {
  const t = themePref();
  if (t === "auto") delete document.documentElement.dataset.theme;
  else document.documentElement.dataset.theme = t;
  const dark = effectiveTheme() === "dark";
  document.getElementById("hljs-light").disabled = dark;
  document.getElementById("hljs-dark").disabled = !dark;
  const btn = document.getElementById("theme-btn");
  btn.textContent = { auto: "Auto", light: "Light", dark: "Dark" }[t];
  btn.setAttribute("aria-label", "Theme: " + t);
  btn.title = "Theme: " + t + " (click to switch)";
  rerenderMermaid();
}
document.getElementById("theme-btn").addEventListener("click", () => {
  const next = THEMES[(THEMES.indexOf(themePref()) + 1) % THEMES.length];
  lsSet("sd-theme", next === "auto" ? null : next);
  applyTheme();
});
matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => { if (themePref() === "auto") applyTheme(); });

// ---------------------------------------------------------------- markdown

const md = window.markdownit({ html: true, linkify: true, typographer: false });
const defaultFence = md.renderer.rules.fence;
md.renderer.rules.fence = (tokens, idx, options, env, self) => {
  const t = tokens[idx];
  const lang = (t.info || "").trim().split(/\s+/)[0].toLowerCase();
  if (lang === "mermaid") {
    return `<div class="mermaid-block pending"><pre class="mermaid-src">${esc(t.content)}</pre>Rendering diagram…</div>\n`;
  }
  return defaultFence(tokens, idx, options, env, self);
};
md.set({
  highlight(code, lang) {
    lang = (lang || "").toLowerCase();
    const alias = { sh: "bash", shell: "bash", console: "bash", golang: "go", yml: "yaml" }[lang] || lang;
    if (alias && alias !== "text" && window.hljs && hljs.getLanguage(alias)) {
      try {
        return `<pre><code class="hljs language-${esc(alias)}">${hljs.highlight(code, { language: alias, ignoreIllegals: true }).value}</code></pre>`;
      } catch (e) { /* fall through */ }
    }
    return `<pre><code class="hljs">${esc(code)}</code></pre>`;
  },
});

function inlineMd(s) { return md.renderInline(String(s || "")); }

// GitHub-style heading slugs, so `file.md#anchor` links from quizzes and lessons work.
function slugify(text) {
  return text.trim().toLowerCase().replace(/[^\p{L}\p{N}\s_-]/gu, "").replace(/\s/g, "-");
}

// ---------------------------------------------------------------- mermaid (lazy)

let mermaidLoading = null;
let mermaidSeq = 0;
function loadMermaid() {
  if (!mermaidLoading) {
    mermaidLoading = new Promise((resolve, reject) => {
      const s = document.createElement("script");
      s.src = "/static/vendor/mermaid.min.js";
      s.onload = () => resolve(window.mermaid);
      s.onerror = () => reject(new Error("could not load Mermaid"));
      document.head.append(s);
    });
  }
  return mermaidLoading;
}

async function renderMermaid(root) {
  const blocks = [...root.querySelectorAll(".mermaid-block")];
  if (!blocks.length) return;
  let mermaid;
  try {
    mermaid = await loadMermaid();
  } catch (e) {
    blocks.forEach((b) => { b.classList.remove("pending"); b.textContent = e.message; });
    return;
  }
  mermaid.initialize({
    startOnLoad: false,
    securityLevel: "strict",
    theme: effectiveTheme() === "dark" ? "dark" : "default",
    fontFamily: getComputedStyle(document.body).fontFamily,
    flowchart: { htmlLabels: true, useMaxWidth: true },
  });
  for (const b of blocks) {
    const src = b.querySelector(".mermaid-src").textContent;
    try {
      const { svg, bindFunctions } = await mermaid.render(`mmd-${++mermaidSeq}`, src);
      b.innerHTML = svg;
      b.append(h("pre", { class: "mermaid-src" }, src));
      if (bindFunctions) bindFunctions(b);
      b.classList.remove("pending");
      b.dispatchEvent(new CustomEvent("mermaid-rendered", { bubbles: true }));
    } catch (e) {
      b.classList.remove("pending");
      b.replaceChildren(h("pre", { class: "mermaid-src" }, src), h("div", { class: "mermaid-error" }, "Diagram error: " + (e.message || e) + "\n\n" + src));
    }
  }
}

function rerenderMermaid() {
  if (!mermaidLoading) return;
  const blocks = document.querySelectorAll(".mermaid-block");
  blocks.forEach((b) => b.classList.add("pending"));
  renderMermaid(document);
}

// ---------------------------------------------------------------- links & routing

let statusCache = null; // last /api/status, for module lookups in link rewriting

function resolveRepoPath(fromPath, href) {
  const base = "http://x/" + fromPath;
  const u = new URL(href, base);
  return { path: decodeURIComponent(u.pathname.replace(/^\//, "")), hash: u.hash };
}

// labModule maps a repo path under labs/ or solutions/ to its module id.
function labModule(p) {
  const m = /^(labs|solutions)\/([a-z0-9]+)(\/|$)/i.exec(p);
  if (!m) return null;
  const want = "labs/" + m[2].toLowerCase();
  const mod = statusCache && statusCache.modules.find((x) => x.id.toLowerCase() === m[2].toLowerCase());
  return mod ? mod.id : m[2].toUpperCase() || want;
}

function routeForPath(p, hash) {
  if (/\.md$/i.test(p)) return "/doc/" + p + (hash || "");
  const lab = labModule(p);
  if (lab) return "/lab/" + lab;
  return null;
}

// fixLinks rewrites links inside rendered Markdown so they work in the app.
function fixLinks(root, docPath) {
  for (const a of root.querySelectorAll("a[href]")) {
    const href = a.getAttribute("href");
    if (/^[a-z]+:/i.test(href)) {
      a.target = "_blank";
      a.rel = "noopener noreferrer";
      continue;
    }
    if (href.startsWith("#")) continue;
    const { path, hash } = resolveRepoPath(docPath, href);
    const route = routeForPath(path, hash);
    if (route) {
      a.setAttribute("href", route);
    } else {
      const span = h("span", { class: "code-ref", title: "Open in your editor: " + path }, ...a.childNodes);
      a.replaceWith(span);
    }
  }
}

function navigate(url, replace) {
  if (replace) history.replaceState(null, "", url);
  else history.pushState(null, "", url);
  render();
}

document.addEventListener("click", (e) => {
  const a = e.target.closest("a[href]");
  if (!a || e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
  if (a.target === "_blank") return;
  const href = a.getAttribute("href");
  if (!href.startsWith("/") || href.startsWith("/static/") || href.startsWith("/api/")) return;
  const u = new URL(href, location.href);
  if (u.pathname === location.pathname && u.hash) return; // in-page anchor: let the browser scroll
  e.preventDefault();
  navigate(href);
});
window.addEventListener("popstate", render);

const routes = [
  [/^\/$/, viewRoadmap],
  [/^\/progress$/, viewProgress],
  [/^\/review$/, () => viewSession({ kind: "review" })],
  [/^\/recap$/, () => viewSession({ kind: "recap" })],
  [/^\/estimate$/, viewEstimate],
  [/^\/module\/([A-Za-z0-9]+)$/, viewModule],
  [/^\/quiz\/([A-Za-z0-9]+)$/, viewQuizStart],
  [/^\/lab\/([A-Za-z0-9]+)$/, viewLabs],
  [/^\/lab\/([A-Za-z0-9]+)\/([A-Za-z0-9_.-]+)\/solution$/, viewSolution],
  [/^\/doc\/(.+)$/, viewDoc],
];

let renderSeq = 0;
let leaveGuard = null; // returns a message when leaving would lose work
async function render() {
  if (leaveGuard) {
    const msg = leaveGuard();
    if (msg && !confirm(msg)) {
      history.pushState(null, "", leaveGuard.url);
      return;
    }
    leaveGuard = null;
  }
  const seq = ++renderSeq;
  if (!statusCache) await refreshNav();
  const path = decodeURIComponent(location.pathname);
  document.querySelectorAll("[data-nav]").forEach((a) => {
    const n = a.dataset.nav;
    a.classList.toggle("active", (n === "roadmap" && (path === "/" || path.startsWith("/module") || path.startsWith("/doc") || path.startsWith("/lab") || path.startsWith("/quiz"))) || path === "/" + n);
  });
  for (const [re, fn] of routes) {
    const m = re.exec(path);
    if (!m) continue;
    app.replaceChildren(h("div", { class: "loading" }, "Loading…"));
    try {
      const view = await fn(...m.slice(1));
      if (seq !== renderSeq) return;
      app.replaceChildren(view);
      window.scrollTo(0, 0);
      afterRender();
    } catch (err) {
      if (seq !== renderSeq) return;
      app.replaceChildren(h("div", { class: "container" }, errorBox(err)));
    }
    refreshNav();
    return;
  }
  app.replaceChildren(h("div", { class: "container" }, h("h1", {}, "Not found"), h("p", {}, h("a", { href: "/" }, "Back to the roadmap"))));
}

function afterRender() {
  if (location.hash) {
    const id = decodeURIComponent(location.hash.slice(1));
    const el = document.getElementById(id);
    if (el) requestAnimationFrame(() => el.scrollIntoView());
  }
}

async function refreshNav() {
  try {
    statusCache = await api("GET", "/api/status");
    const due = statusCache.report.due_reviews;
    const badge = document.getElementById("due-count");
    badge.hidden = !due;
    badge.textContent = due;
  } catch (e) { /* nav badge is best-effort */ }
}

function stateLabel(st) {
  return {
    planned: "Planned",
    locked: "Locked",
    available: "Unlocked",
    in_progress: "In progress",
    passed: "Passed",
    needs_refresh: "Needs refresh",
  }[st] || st;
}
const stateGlyph = { planned: "○", locked: "🔒", available: "○", in_progress: "●", passed: "✓", needs_refresh: "↻" };

function stateBadge(ms) {
  let state = ms.state;
  let label = stateLabel(state);
  if (state === "planned") label = "Planned · unlocked";
  if (state === "locked" && !ms.written) label = "Locked · planned";
  return h("span", { class: "badge " + state, title: ms.refresh_reason || "" }, h("span", { "aria-hidden": "true" }, stateGlyph[state] || ""), label);
}

// commandRoute maps a CLI command mentioned in a recommendation to an in-app page.
function commandRoute(cmd) {
  let m;
  if ((m = /^sd quiz ([A-Za-z]\d+)/.exec(cmd))) return "/quiz/" + m[1].toUpperCase();
  if ((m = /^sd lab ([A-Za-z]\d+)/.exec(cmd))) return "/lab/" + m[1].toUpperCase();
  if ((m = /^sd status ([A-Za-z]\d+)/.exec(cmd))) return "/module/" + m[1].toUpperCase();
  if (/^sd review/.test(cmd)) return "/review";
  if (/^sd recap/.test(cmd)) return "/recap";
  if (/^sd estimate/.test(cmd)) return "/estimate";
  if (/^sd (status|progress)/.test(cmd)) return "/progress";
  return null;
}

// linkify turns `sd …` code spans into links to the matching page. With docs, code
// spans naming a module file (`modules/…/design-exercise.md`) link to the reader too.
function linkify(el, docs) {
  for (const code of el.querySelectorAll("code")) {
    const t = code.textContent.trim();
    const r = commandRoute(t) || (docs && /^(modules|docs|labs)\/[\w./-]+\.md$/.test(t) ? "/doc/" + t : null);
    if (r && !code.closest("a")) code.replaceWith(h("a", { href: r }, code.cloneNode(true)));
  }
  return el;
}

// richText renders a status line from the Go side (a recommendation or a gate action),
// marking bare CLI commands and repo paths as code so they become links.
function richText(s, tag = "span") {
  const marked = String(s || "")
    .split("`")
    .map((part, i) => (i % 2 ? "`" + part + "`" : part
      .replace(/\bsd (?:quiz|lab|status|review|recap|estimate|progress)(?: -n \d+)?(?: [A-Z]\d+)?/g, (m) => "`" + m + "`")
      .replace(/\b(?:modules|docs|labs)\/[\w./-]+\.md\b/g, (m) => "`" + m + "`")))
    .join("");
  return linkify(h(tag, { html: inlineMd(marked) }), true);
}

// ---------------------------------------------------------------- roadmap

async function viewRoadmap() {
  const st = await api("GET", "/api/status");
  statusCache = st;
  setTitle("Roadmap");
  const rep = st.report;
  const byId = Object.fromEntries(st.modules.map((m) => [m.id, m]));
  const nextIds = new Set();
  for (const n of rep.next) {
    const m = /^([A-Z]\d+) /.exec(n);
    if (m && byId[m[1]]) nextIds.add(m[1]);
  }

  const passed = st.modules.filter((m) => m.gate_passed_at).length;
  const core = st.modules.filter((m) => m.core);
  const corePassed = core.filter((m) => m.gate_passed_at).length;
  const es = rep.estimate;

  const nextCard = h("section", { class: "card" },
    h("div", { class: "eyebrow" }, "Next recommended step"),
    h("ol", { class: "next-list" }, rep.next.map((n) => h("li", {}, richText(n)))),
  );
  const tiles = h("div", { class: "grid tiles" },
    tile("Gates passed", `${corePassed}/${core.length}`, "core modules", corePassed / Math.max(core.length, 1)),
    tile("Core hours behind gates", `${Math.round(rep.hours_passed)}h`, `of ≈ ${Math.round(rep.hours_total)}h`, rep.hours_passed / Math.max(rep.hours_total, 1)),
    linkTile("/review", "Reviews due", String(rep.due_reviews), rep.next_review_due ? "next scheduled " + fmtDate(rep.next_review_due) : "spaced repetition"),
    linkTile("/estimate", "Estimation", es.mastered ? "Mastered" : pct(es.window_rate), `${es.window} recent drills` + (es.kinds_missing && es.kinds_missing.length ? ` · ${es.kinds_missing.length} kinds untried` : "")),
  );

  const head = h("div", { class: "page-head" },
    h("div", { class: "eyebrow" }, "Roadmap"),
    h("h1", {}, st.title),
    h("p", {}, lastActive(st)),
  );
  const recap = rep.recap_due
    ? h("div", { class: "notice warn" }, h("b", {}, `Welcome back — ${rep.gap_days} days away. `), "Start with a recap warm-up before new material. ", h("a", { class: "btn small primary", href: "/recap" }, "Start recap"))
    : null;

  // Track lanes
  const tracksView = h("div", {});
  for (const t of st.tracks) {
    const mods = st.modules.filter((m) => m.track === t.id).sort((a, b) => idLess(a.id, b.id));
    if (!mods.length) continue;
    const done = mods.filter((m) => m.gate_passed_at).length;
    tracksView.append(h("section", { class: "track" },
      h("div", { class: "track-head" },
        h("h2", {}, h("span", { class: "track-letter" }, t.id), t.title),
        h("span", { class: "track-progress" }, `${done}/${mods.length} passed`)),
      h("div", { class: "grid cards" }, mods.map((m) => moduleCard(m, nextIds.has(m.id)))),
    ));
  }

  const graphView = h("div", {});
  let graphDone = false;
  const tabs = h("div", { class: "tabs", role: "tablist" });
  const body = h("div", {}, tracksView);
  const mkTab = (label, key) => h("button", {
    type: "button", role: "tab", class: key === "tracks" ? "active" : "",
    onclick: (e) => {
      tabs.querySelectorAll("button").forEach((b) => b.classList.toggle("active", b === e.currentTarget));
      lsSet("sd-roadmap-tab", key);
      body.replaceChildren(key === "tracks" ? tracksView : graphView);
      if (key === "graph" && !graphDone) { graphDone = true; buildGraph(graphView, st); }
    },
  }, label);
  tabs.append(mkTab("By track", "tracks"), mkTab("Dependency graph", "graph"));
  if (lsGet("sd-roadmap-tab") === "graph") setTimeout(() => tabs.children[1].click());

  return h("div", { class: "container" },
    head, recap,
    h("div", { class: "hero" }, nextCard, tiles),
    tabs, body,
    h("p", { class: "muted small" }, "Planning detail and the reasoning behind the order: ", h("a", { href: "/doc/SYLLABUS.md" }, "SYLLABUS.md"), " · ", h("a", { href: "/doc/README.md" }, "README.md"), ` · ${passed} gate(s) passed overall.`),
  );
}

function lastActive(st) {
  if (!st.last_active_at) return "No activity yet. Start with A0 and B0 — both are unlocked.";
  const gap = st.report.gap_days;
  return gap === 0 ? "Last active today." : `Last active ${gap} day${gap === 1 ? "" : "s"} ago (${fmtDate(st.last_active_at)}).`;
}

function tile(label, value, foot, frac) {
  return h("div", { class: "card tile" },
    h("span", { class: "label" }, label),
    h("span", { class: "value" }, value),
    h("span", { class: "foot" }, foot),
    frac != null ? h("div", { class: "meter", role: "img", "aria-label": pct(frac) }, h("span", { style: { width: Math.min(100, frac * 100) + "%" } })) : null,
  );
}
function linkTile(href, label, value, foot) {
  const t = tile(label, value, foot);
  return h("a", { href, class: "card tile", style: { textDecoration: "none", color: "inherit" } }, ...t.childNodes);
}

function moduleCard(m, isNext) {
  const cls = ["card", "mod-card", "is-" + m.state.replace("_", "-")];
  if (m.state === "planned") cls.push("is-planned");
  if (isNext) cls.push("is-next");
  const meta = [];
  meta.push(h("span", {}, `${Math.round(m.hours)}h`));
  if (!m.core) meta.push(h("span", { class: "badge stretch" }, "Stretch"));
  if (m.state === "locked") meta.push(h("span", {}, "needs " + (m.missing_prereqs || []).join(", ")));
  if (m.state === "passed" && m.due_cards) meta.push(h("span", {}, `${m.due_cards} reviews due`));
  if (m.review_overdue) meta.push(h("span", {}, "review overdue"));
  return h("a", { href: "/module/" + m.id, class: cls.join(" ") },
    h("div", { class: "top" }, h("span", { class: "mod-id" }, m.id), isNext ? h("span", { class: "badge next" }, "Next") : stateBadge(m)),
    h("div", { class: "mod-title" }, m.title),
    isNext ? h("div", {}, stateBadge(m)) : null,
    (m.state === "in_progress" || m.state === "needs_refresh") ? compDots(m.components) : null,
    h("div", { class: "mod-meta" }, meta),
  );
}

function compDots(cs) {
  return h("div", { class: "comp-dots" }, (cs || []).map((c) =>
    h("span", { class: "comp-dot" + (c.met ? " met" : ""), title: c.detail }, (c.met ? "✓ " : "") + c.name)));
}

async function buildGraph(container, st) {
  const classes = { passed: "passed", needs_refresh: "refresh", in_progress: "progress", available: "open", planned: "planned", locked: "locked" };
  const lines = ["flowchart LR"];
  for (const m of st.modules) {
    const title = m.title.replace(/["<>#]/g, "").slice(0, 34);
    lines.push(`  ${m.id}["${m.id} · ${title}"]:::${classes[m.state] || "locked"}`);
  }
  for (const m of st.modules) for (const p of m.prerequisites || []) if (st.modules.some((x) => x.id === p)) lines.push(`  ${p} --> ${m.id}`);
  const dark = effectiveTheme() === "dark";
  const css = dark
    ? { passed: "fill:#1d2f1d,stroke:#0ca30c", refresh: "fill:#3a2a22,stroke:#ec835a", progress: "fill:#36301d,stroke:#fab219", open: "fill:#1f2d40,stroke:#3987e5", planned: "fill:#222220,stroke:#4a4a46,stroke-dasharray:4 3", locked: "fill:#2a2a28,stroke:#4a4a46,color:#9b9a92" }
    : { passed: "fill:#e5f5e5,stroke:#0ca30c", refresh: "fill:#fcebe3,stroke:#ec835a", progress: "fill:#fdf3dc,stroke:#fab219", open: "fill:#e6f0fc,stroke:#2a78d6", planned: "fill:#fcfcfb,stroke:#cfccc4,stroke-dasharray:4 3", locked: "fill:#f4f3f0,stroke:#cfccc4,color:#74726c" };
  for (const [k, v] of Object.entries(css)) lines.push(`  classDef ${k} ${v}`);
  const block = h("div", { class: "mermaid-block pending graph-wrap" }, h("pre", { class: "mermaid-src" }, lines.join("\n")), "Drawing the graph…");
  const legend = h("div", { class: "legend" }, ["passed", "needs_refresh", "in_progress", "available", "planned", "locked"].map((s) => stateBadge({ state: s, written: true })));
  container.replaceChildren(legend, block, h("p", { class: "muted small" }, "Arrows point from a prerequisite to the module it unlocks. Click a module to open it."));
  block.addEventListener("mermaid-rendered", () => {
    for (const node of block.querySelectorAll("g.node")) {
      const m = /flowchart-([A-Z]\d+)-/.exec(node.id);
      if (m) node.addEventListener("click", () => navigate("/module/" + m[1]));
    }
  });
  await renderMermaid(container);
}

// ---------------------------------------------------------------- module page

async function viewModule(id) {
  const d = await api("GET", "/api/modules/" + encodeURIComponent(id));
  const m = d.module, s = d.status;
  setTitle(m.id + " " + m.title);
  const track = statusCache && statusCache.tracks.find((t) => t.id === m.track);

  const head = h("div", { class: "page-head" },
    h("div", { class: "crumbs" }, h("a", { href: "/" }, "Roadmap"), " / ", `Track ${m.track}${track ? " — " + track.title : ""}`),
    h("div", { class: "mod-head" },
      h("h1", {}, h("span", { class: "mod-id", style: { fontSize: "inherit" } }, m.id), " ", m.title),
      stateBadge(s)),
    h("p", {}, m.summary),
    h("div", { class: "facts" },
      h("span", {}, h("b", {}, `${Math.round(s.hours)}h`), ` (core ${m.hours.core}, stretch ${m.hours.stretch})`),
      h("span", {}, m.core ? "CORE module" : "STRETCH module"),
      h("span", {}, "Prerequisites: ", (m.prerequisites || []).length ? m.prerequisites.map((p, i) => [i ? ", " : "", h("a", { href: "/module/" + p }, p)]) : "none"),
      d.dependents.length ? h("span", {}, "Unlocks: ", d.dependents.map((p, i) => [i ? ", " : "", h("a", { href: "/module/" + p }, p)])) : null,
    ),
  );

  const out = h("div", { class: "container" }, head);
  if (s.state === "locked") {
    out.append(h("div", { class: "notice warn" }, `Locked until ${s.missing_prereqs.join(", ")} pass${s.missing_prereqs.length === 1 ? "es" : ""} its gate. You can read ahead, but the tutor will steer you to the prerequisites first.`));
  }
  if (s.state === "needs_refresh") {
    out.append(h("div", { class: "notice warn" }, h("b", {}, "Needs refresh: "), s.refresh_reason, ". Re-read the unit wrap-ups, then pass a full quiz to clear it."));
  }
  if (!m.status || m.status !== "written") {
    out.append(h("div", { class: "notice info" }, "This module is planned but not written yet. Its outcomes are below; author it with MODULE_TEMPLATE.md in a new session and it will appear here automatically."));
  }

  const left = h("div", {});
  const right = h("div", {});
  if (d.docs && d.docs.length) {
    left.append(h("h2", { class: "section" }, "Lessons"), h("div", { class: "card", style: { padding: "6px" } }, h("ul", { class: "doc-list" }, d.docs.map(docItem))));
  }
  if (m.outcomes && m.outcomes.length) {
    left.append(h("h2", { class: "section" }, "Outcomes"), h("div", { class: "card" }, h("ul", { class: "outcomes" },
      m.outcomes.map((o) => h("li", {}, o.text, " ", h("span", { class: "badge " + (o.core ? "core" : "stretch") }, o.core ? "Core" : "Stretch"))))));
  }

  right.append(h("h2", { class: "section" }, "Mastery gate"), gateCard(m, s));
  if (d.quiz) {
    const q = d.quiz;
    right.append(h("h2", { class: "section" }, "Quiz"), h("div", { class: "card" },
      q.error ? errorBox(q.error) : h("p", { style: { marginTop: 0 } }, `${q.pool} questions in the pool; a gated attempt draws ${q.full}.`),
      h("div", { class: "row" },
        h("a", { class: "btn primary", href: "/quiz/" + m.id }, "Take the quiz"),
        h("a", { class: "btn ghost", href: "/review" }, "Review due cards")),
      d.quiz_attempts.length ? attemptsTable(d.quiz_attempts, statusCache ? statusCache.policy.quiz_pass_threshold : 0.85) : h("p", { class: "empty" }, "No gated attempts yet."),
    ));
  }
  if (m.labs && m.labs.length) {
    right.append(h("h2", { class: "section" }, "Labs"), h("div", { class: "card" },
      h("ul", { class: "outcomes" }, m.labs.map((l) => h("li", {}, h("code", {}, l.package), " — ", l.title, " ", h("span", { class: "badge " + (l.core ? "core" : "stretch") }, l.core ? "Core" : "Stretch")))),
      m.status === "written" ? h("a", { class: "btn", href: "/lab/" + m.id }, "Open labs & run tests") : null));
  }
  out.append(h("div", { class: "grid two", style: { alignItems: "start" } }, left, right));
  return out;
}

function docItem(d) {
  const num = { readme: "≡", unit: String(d.n), design: "✎", teachback: "✍" }[d.kind] || "•";
  const sub = [];
  if (d.kind === "unit") sub.push(`${d.core_hours}h core` + (d.stretch_hours ? ` + ${d.stretch_hours}h stretch` : ""));
  if (!d.available) sub.push("file missing");
  const prog = d.predicts
    ? h("span", { class: "pill-progress" + (d.answered === d.predicts ? " done" : ""), title: "predict blocks answered" }, `${d.answered}/${d.predicts} predictions${d.answered === d.predicts ? " ✓" : ""}`)
    : h("span", {});
  return h("li", {}, h("a", { href: "/doc/" + d.path },
    h("span", { class: "doc-num" }, num),
    h("span", {}, h("div", { class: "doc-title" }, d.kind === "unit" ? `Unit ${d.n} — ${d.title}` : d.title), sub.length ? h("div", { class: "doc-sub" }, sub.join(" · ")) : null),
    prog));
}

function componentAction(m, c) {
  switch (c.name) {
    case "quiz": return h("a", { class: "btn small", href: "/quiz/" + m.id }, "Quiz");
    case "lab": return h("a", { class: "btn small", href: "/lab/" + m.id }, "Labs");
    case "design": return m.paths.design_exercise ? h("a", { class: "btn small", href: "/doc/" + m.paths.design_exercise }, "Exercise") : null;
    case "teachback": return m.paths.teachback ? h("a", { class: "btn small", href: "/doc/" + m.paths.teachback }, "Prompt") : null;
    default: return null;
  }
}

function compName(n) {
  return { quiz: "Quiz", lab: "Labs", design: "Design exercise", teachback: "Teach-back", mocks: "Mocks" }[n] || n;
}

function gateCard(m, s) {
  const items = (s.components || []).map((c) => h("li", {},
    h("span", { class: "check" + (c.met ? " met" : ""), "aria-label": c.met ? "met" : "not met" }, c.met ? "✓" : ""),
    h("div", {},
      h("div", { class: "check-name" }, compName(c.name)),
      h("div", { class: "check-detail" }, c.detail),
      !c.met && c.action ? h("div", { class: "check-detail" }, "→ ", richText(c.action)) : null),
    m.status === "written" ? componentAction(m, c) : null));
  const passedNote = s.gate_passed_at ? h("div", { class: "notice good" }, `✓ Gate passed ${fmtDate(s.gate_passed_at)}.`) : null;
  const tutorNote = h("p", { class: "muted small", style: { marginBottom: 0 } }, "Design and teach-back are assessed by the tutor in a Claude session (see TUTOR.md) and recorded with ", h("code", {}, "sd record"), ".");
  return h("div", { class: "card" }, passedNote, h("ul", { class: "checklist" }, items), tutorNote);
}

function attemptsTable(atts, thr) {
  return h("div", { class: "table-wrap", style: { marginTop: "12px" } }, h("table", { class: "data" },
    h("thead", {}, h("tr", {}, h("th", {}, "When"), h("th", { class: "num" }, "Score"), h("th", {}, "Result"))),
    h("tbody", {}, atts.slice().reverse().map((a) => h("tr", {},
      h("td", {}, fmtDateTime(a.at)),
      h("td", { class: "num" }, `${a.correct}/${a.total} · ${pct(a.score)}`),
      h("td", {}, a.score + 1e-9 >= thr ? h("span", { class: "badge pass" }, "✓ Pass") : h("span", { class: "badge fail" }, "✗ Below"))))),
  ));
}

// ---------------------------------------------------------------- lesson reader

async function viewDoc(path) {
  const d = await api("GET", "/api/doc?path=" + encodeURIComponent(path));
  const body = h("article", { class: "prose", html: md.render(d.markdown) });
  // Heading ids (GitHub slugs) and the table of contents.
  const seen = {};
  const toc = [];
  for (const el of body.querySelectorAll("h1, h2, h3")) {
    let slug = slugify(el.textContent);
    if (seen[slug] != null) slug = `${slug}-${++seen[slug]}`;
    else seen[slug] = 0;
    el.id = slug;
    if (el.tagName !== "H1") toc.push({ id: slug, text: el.textContent, level: el.tagName.toLowerCase() });
  }
  const title = (body.querySelector("h1") || {}).textContent || path;
  setTitle(title);
  fixLinks(body, d.path);
  linkify(body);
  taskLists(body);
  addCopyButtons(body);
  setupPredictions(body, d);

  const tocNav = h("nav", { class: "toc", "aria-label": "On this page" });
  if (d.module) {
    tocNav.append(h("h4", {}, "Module"), h("a", { href: "/module/" + d.module }, `${d.module} — ${d.module_title}`));
  }
  const progressLine = h("div", { class: "doc-progress" });
  if (d.predict_ids.length) tocNav.append(h("h4", {}, "Predictions"), progressLine);
  const updateProgress = () => {
    const n = body.querySelectorAll(".predict.answered").length;
    progressLine.textContent = `${n} of ${d.predict_ids.length} written`;
  };
  body.addEventListener("prediction-saved", updateProgress);
  updateProgress();
  const h2s = toc.filter((t) => t.level === "h2");
  if (h2s.length) {
    tocNav.append(h("h4", {}, "On this page"));
    for (const t of toc) {
      if (t.level === "h3" && toc.length > 40) continue;
      tocNav.append(h("a", { href: "#" + t.id, class: t.level, dataset: { target: t.id } }, t.text));
    }
  }
  const crumbs = h("div", { class: "crumbs", style: { marginBottom: "18px", fontFamily: "var(--font)", fontSize: "14px" } },
    h("a", { href: "/" }, "Roadmap"), " / ",
    d.module ? [h("a", { href: "/module/" + d.module }, d.module), " / "] : null,
    h("span", {}, d.path));
  const view = h("div", { class: "reader" }, tocNav, h("div", {}, crumbs, body));
  requestAnimationFrame(() => {
    renderMermaid(body);
    trackToc(tocNav, body);
  });
  return view;
}

function taskLists(root) {
  for (const li of root.querySelectorAll("li")) {
    const first = li.firstChild;
    if (first && first.nodeType === 3) {
      const m = /^\[( |x|X)\]\s/.exec(first.textContent);
      if (m) {
        first.textContent = first.textContent.slice(m[0].length);
        li.prepend(h("input", { type: "checkbox", disabled: true, checked: m[1] !== " " }));
        li.classList.add("task");
      }
    }
  }
}

function addCopyButtons(root) {
  for (const pre of root.querySelectorAll("pre")) {
    if (pre.classList.contains("mermaid-src")) continue;
    const btn = h("button", { type: "button", class: "copy-btn", onclick: async () => {
      try { await navigator.clipboard.writeText(pre.querySelector("code") ? pre.querySelector("code").textContent : pre.textContent); btn.textContent = "Copied"; }
      catch (e) { btn.textContent = "Copy failed"; }
      setTimeout(() => { btn.textContent = "Copy"; }, 1500);
    } }, "Copy");
    pre.append(btn);
  }
}

function trackToc(nav, body) {
  const links = new Map([...nav.querySelectorAll("a[data-target]")].map((a) => [a.dataset.target, a]));
  if (!links.size || !("IntersectionObserver" in window)) return;
  const obs = new IntersectionObserver((entries) => {
    for (const e of entries) {
      if (e.isIntersecting) {
        links.forEach((a) => a.classList.remove("current"));
        const a = links.get(e.target.id);
        if (a) { a.classList.add("current"); a.scrollIntoView({ block: "nearest" }); }
      }
    }
  }, { rootMargin: "-70px 0px -75% 0px" });
  body.querySelectorAll("h2, h3").forEach((el) => links.has(el.id) && obs.observe(el));
}

// setupPredictions adds an answer box to every predict block and keeps its reveal
// closed until the learner has written and saved an answer.
function setupPredictions(body, d) {
  for (const box of body.querySelectorAll(".predict[data-predict-id]")) {
    const id = box.dataset.predictId;
    const reveal = body.querySelector(`details[data-kind="reveal"][data-for="${CSS.escape(id)}"]`);
    const saved = d.predictions[id];
    const draftKey = "sd-draft:" + id;
    const ta = h("textarea", { id: "ans-" + id, placeholder: "Write your answer here before you reveal. Being wrong is the useful part.", "aria-label": "Your prediction for " + id });
    const msg = h("span", { class: "predict-msg" });
    const btn = h("button", { type: "button", class: "btn primary small" }, "Save & reveal");
    const form = h("div", { class: "predict-form" }, h("label", { for: "ans-" + id }, "Your prediction"), ta, h("div", { class: "row" }, btn, msg));
    box.append(form);

    let unlocked = false;
    const unlock = (pred, open) => {
      unlocked = true;
      box.classList.add("answered");
      ta.value = pred.answer;
      ta.readOnly = true;
      msg.className = "predict-msg";
      msg.textContent = `Saved ${fmtDateTime(pred.at)} — kept as written so you can compare.`;
      btn.textContent = reveal ? "Show reveal" : "Saved";
      btn.classList.remove("primary");
      btn.disabled = !reveal;
      if (reveal) {
        reveal.classList.remove("reveal-locked");
        if (open) {
          reveal.open = true;
          reveal.scrollIntoView({ behavior: "smooth", block: "start" });
        }
      }
      body.dispatchEvent(new CustomEvent("prediction-saved"));
    };
    if (reveal) {
      reveal.classList.add("reveal-locked");
      const summary = reveal.querySelector("summary");
      const nudge = (e) => {
        if (unlocked) return;
        e.preventDefault();
        reveal.open = false;
        box.classList.remove("shake");
        void box.offsetWidth;
        box.classList.add("shake");
        msg.className = "predict-msg err";
        msg.textContent = "Write your prediction first, then reveal.";
        ta.focus({ preventScroll: false });
        box.scrollIntoView({ behavior: "smooth", block: "center" });
      };
      if (summary) summary.addEventListener("click", nudge);
      // Find-in-page and other paths can open a <details>; close it again while locked.
      reveal.addEventListener("toggle", () => { if (!unlocked && reveal.open) reveal.open = false; });
    }
    if (saved) {
      unlock(saved, false);
    } else {
      ta.value = lsGet(draftKey) || "";
      ta.addEventListener("input", () => lsSet(draftKey, ta.value || null));
    }
    btn.addEventListener("click", async () => {
      if (unlocked) {
        if (reveal) { reveal.open = true; reveal.scrollIntoView({ behavior: "smooth", block: "start" }); }
        return;
      }
      const answer = ta.value.trim();
      if (!answer) {
        msg.className = "predict-msg err";
        msg.textContent = "Write something first: even a guess with your reasoning.";
        ta.focus();
        return;
      }
      btn.disabled = true;
      msg.className = "predict-msg";
      msg.textContent = "Saving…";
      try {
        const r = await api("PUT", "/api/predictions/" + encodeURIComponent(id), { answer });
        lsSet(draftKey, null);
        btn.disabled = false;
        unlock(r.prediction, true);
      } catch (e) {
        btn.disabled = false;
        msg.className = "predict-msg err";
        msg.textContent = e.message;
      }
    });
    ta.addEventListener("keydown", (e) => { if ((e.metaKey || e.ctrlKey) && e.key === "Enter") btn.click(); });
  }
}

// ---------------------------------------------------------------- quiz / review / recap

async function viewQuizStart(id) {
  const d = await api("GET", "/api/modules/" + encodeURIComponent(id));
  const m = d.module;
  setTitle(m.id + " quiz");
  if (!d.quiz || d.quiz.error) {
    return h("div", { class: "narrow" }, h("h1", {}, m.id + " quiz"), errorBox(d.quiz ? d.quiz.error : "This module has no quiz yet."));
  }
  const thr = statusCache ? statusCache.policy.quiz_pass_threshold : 0.85;
  const quizComp = (d.status.components || []).find((c) => c.name === "quiz");
  const nInput = h("input", { type: "number", min: 1, max: d.quiz.full - 1, value: Math.min(5, d.quiz.full - 1), style: { width: "80px" } });
  return h("div", { class: "narrow" },
    h("div", { class: "page-head" },
      h("div", { class: "crumbs" }, h("a", { href: "/" }, "Roadmap"), " / ", h("a", { href: "/module/" + m.id }, m.id), " / Quiz"),
      h("h1", {}, `${m.id} quiz — ${d.quiz.title || m.title}`),
      h("p", {}, "Commit to an answer before you look anything up — that's the point. Each answer is graded immediately with an explanation; nothing is recorded if you quit midway.")),
    d.status.state === "locked" ? h("div", { class: "notice warn" }, `${m.id} is locked (needs ${d.status.missing_prereqs.join(", ")}). You can practise, but the tutor will steer you to the prerequisites first.`) : null,
    h("div", { class: "grid two" },
      h("div", { class: "card" },
        h("h3", {}, "Gated attempt"),
        h("p", { class: "small muted" }, `${d.quiz.full} questions, least-seen first. Counts toward the gate at ≥ ${pct(thr)}.`),
        quizComp ? h("p", { class: "small" }, quizComp.detail) : null,
        h("button", { class: "btn primary", type: "button", onclick: () => startQuiz(m.id, 0) }, "Start gated attempt")),
      h("div", { class: "card" },
        h("h3", {}, "Short practice"),
        h("p", { class: "small muted" }, "Fewer questions: answers join your review deck but don't count for the gate."),
        h("div", { class: "row" }, h("label", { class: "field" }, "Questions", nInput),
          h("button", { class: "btn", type: "button", style: { alignSelf: "flex-end" }, onclick: () => startQuiz(m.id, Number(nInput.value) || 5) }, "Practise"))),
    ));
}

async function startQuiz(module, n) {
  app.replaceChildren(h("div", { class: "loading" }, "Drawing questions…"));
  try {
    const sess = await api("POST", "/api/sessions", { kind: "quiz", module, n });
    app.replaceChildren(runSession(sess));
  } catch (e) {
    app.replaceChildren(h("div", { class: "narrow" }, errorBox(e)));
  }
}

async function viewSession({ kind }) {
  setTitle(kind === "recap" ? "Recap" : "Review");
  const sess = await api("POST", "/api/sessions", { kind });
  if (!sess.items.length) {
    return h("div", { class: "narrow" },
      h("div", { class: "page-head" }, h("h1", {}, sess.title)),
      h("div", { class: "card" }, h("p", { style: { margin: 0 } }, sess.empty || "Nothing to do."),
        h("div", { class: "q-actions" }, h("a", { class: "btn", href: "/progress" }, "See the review queue"), h("a", { class: "btn ghost", href: "/" }, "Roadmap"))));
  }
  return runSession(sess);
}

// runSession drives one quiz/review/recap session question by question.
function runSession(sess) {
  const wrap = h("div", { class: "narrow" });
  const intro = {
    quiz: sess.gated ? `Gated attempt · ${sess.items.length} questions · pass mark ${pct(sess.threshold)}` : `Short practice · ${sess.items.length} questions · not recorded for the gate`,
    review: `${sess.items.length} cards due. Misses come back tomorrow; hits move to a longer interval. Each answer is saved as you go.`,
    recap: `${sess.items.length} warm-up questions from what you last studied. Misses go back into tomorrow's review.`,
  }[sess.kind];
  wrap.append(h("div", { class: "page-head" },
    sess.module ? h("div", { class: "crumbs" }, h("a", { href: "/" }, "Roadmap"), " / ", h("a", { href: "/module/" + sess.module }, sess.module), " / Quiz") : null,
    h("h1", {}, sess.title), h("p", {}, intro)));
  if (sess.locked_by) wrap.append(h("div", { class: "notice warn" }, `Locked module (needs ${sess.locked_by.join(", ")}): practise freely, but do the prerequisites first.`));
  const stage = h("div", {});
  wrap.append(stage);

  let i = 0, correct = 0, finished = false;
  leaveGuard = () => (!finished && i > 0 && sess.kind === "quiz" ? "Leave this quiz? Nothing will be recorded." : null);
  leaveGuard.url = location.pathname;

  const quit = async () => {
    if (sess.kind === "quiz" && !confirm("Quit this attempt? Nothing will be recorded.")) return;
    finished = true;
    leaveGuard = null;
    try {
      const r = await api("POST", `/api/sessions/${sess.id}/finish`, {});
      if (r.summary) { showSummary(stage, sess, r.summary); return; }
    } catch (e) { /* session may have expired */ }
    navigate(sess.module ? "/module/" + sess.module : "/");
  };

  const show = () => {
    const it = sess.items[i];
    const selected = new Set();
    const isMulti = it.type === "multi";
    const prompt = h("div", { class: "q-prompt" + (/\n {2,}\S|\t/.test(it.prompt) ? " has-code" : "") }, it.prompt);
    const head = h("div", {},
      h("div", { class: "q-progress" },
        h("span", {}, `Question ${i + 1} of ${sess.items.length}` + (i ? ` · ${correct} correct` : "")),
        h("span", { class: "q-tags" }, h("span", { class: "tag" }, it.id), it.tags.map((t) => h("span", { class: "tag" }, t)))),
      h("div", { class: "q-bar" }, h("span", { style: { width: (i / sess.items.length) * 100 + "%" } })));
    const card = h("div", { class: "card qcard" }, head, prompt);
    let input = null;
    let choiceEls = [];
    if (it.type === "numeric") {
      input = h("input", { type: "text", inputmode: "decimal", autocomplete: "off", placeholder: "e.g. 1.2k, 3.5M, 2e6, 40 TB", "aria-label": "Your answer" });
      card.append(h("div", { class: "q-hint" }, "Suffixes like 1.2k, 3M, 2e6 and units like 40TB or 12 Gbps work."),
        h("div", { class: "num-input" }, input, it.unit ? h("span", { class: "unit" }, it.unit) : null));
    } else {
      card.append(h("div", { class: "q-hint" }, isMulti ? "Select all that apply. Keys 1–9 toggle, Enter submits." : "Pick one. Keys 1–9 select, Enter submits."));
      choiceEls = it.choices.map((c, k) => {
        const el = h("button", { type: "button", class: "choice" + (isMulti ? " multi" : ""), "aria-pressed": "false" },
          h("span", { class: "key" }, String(k + 1)), h("span", {}, c), h("span", { class: "mark" }));
        el.addEventListener("click", () => {
          if (el.classList.contains("graded")) return;
          if (!isMulti) { selected.clear(); choiceEls.forEach((x) => { x.classList.remove("selected"); x.setAttribute("aria-pressed", "false"); }); }
          if (selected.has(k + 1)) { selected.delete(k + 1); el.classList.remove("selected"); el.setAttribute("aria-pressed", "false"); }
          else { selected.add(k + 1); el.classList.add("selected"); el.setAttribute("aria-pressed", "true"); }
        });
        return el;
      });
      card.append(h("div", { class: "choices" }, choiceEls));
    }
    const err = h("div", { class: "predict-msg err", role: "alert" });
    const submit = h("button", { type: "button", class: "btn primary" }, "Check answer ", h("span", { class: "kbd" }, "↵"));
    const skip = h("button", { type: "button", class: "btn ghost" }, "Skip");
    const quitBtn = h("button", { type: "button", class: "btn ghost", style: { marginLeft: "auto" }, onclick: quit }, sess.kind === "quiz" ? "Quit" : "End session");
    const actions = h("div", { class: "q-actions" }, submit, skip, quitBtn);
    card.append(err, actions);
    stage.replaceChildren(card);
    (input || choiceEls[0] || submit).focus({ preventScroll: true });

    let graded = false;
    const send = async (body) => {
      if (graded) return;
      err.textContent = "";
      submit.disabled = skip.disabled = true;
      let r;
      try {
        r = await api("POST", `/api/sessions/${sess.id}/answers`, Object.assign({ index: i }, body));
      } catch (e) {
        err.textContent = e.message;
        submit.disabled = skip.disabled = false;
        return;
      }
      graded = true;
      const res = r.result;
      if (res.correct) correct++;
      choiceEls.forEach((el, k) => {
        el.classList.add("graded");
        const want = (res.correct_choices || []).includes(k + 1);
        const picked = selected.has(k + 1);
        if (want) { el.classList.add("is-correct"); el.querySelector(".mark").textContent = picked ? "✓ correct" : "✓ answer"; }
        else if (picked) { el.classList.add("is-wrong"); el.querySelector(".mark").textContent = "✗ your pick"; }
      });
      if (input) input.disabled = true;
      const fb = h("div", { class: "feedback " + (res.correct ? "ok" : "no"), role: "status" },
        h("div", { class: "verdict" }, res.correct ? "✓ Correct" : body.skip ? "Skipped" : "✗ Not quite"),
        !res.correct && it.type === "numeric" ? h("div", { class: "meta" }, `You: ${res.given} · Expected: ${res.expected}`) : null,
        res.correct && it.type === "numeric" ? h("div", { class: "meta" }, `Reference: ${res.expected}`) : null,
        res.note ? h("div", { class: "meta" }, res.note) : null,
        h("div", { class: "expl" }, res.explanation),
        res.ref_path ? h("div", { class: "meta" }, "See: ", h("a", { href: "/doc/" + res.ref_path + (res.ref_anchor ? "#" + res.ref_anchor : ""), target: "_blank" }, res.ref + " ↗")) : null);
      const nextBtn = h("button", { type: "button", class: "btn primary" }, r.done ? "See results " : "Next question ", h("span", { class: "kbd" }, "↵"));
      nextBtn.addEventListener("click", () => {
        if (r.done) { finished = true; leaveGuard = null; showSummary(stage, sess, r.summary); }
        else { i++; show(); }
      });
      actions.replaceChildren(nextBtn, quitBtn);
      if (r.done) quitBtn.remove();
      card.insertBefore(fb, err);
      nextBtn.focus({ preventScroll: true });
      fb.scrollIntoView({ behavior: "smooth", block: "nearest" });
    };
    submit.addEventListener("click", () => {
      if (it.type === "numeric") {
        if (!input.value.trim()) { err.textContent = "Enter a number (or Skip)."; return; }
        send({ input: input.value });
      } else {
        if (!selected.size) { err.textContent = "Pick a choice (or Skip)."; return; }
        send({ choices: [...selected].sort((a, b) => a - b) });
      }
    });
    skip.addEventListener("click", () => send({ skip: true }));
    if (input) input.addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); submit.click(); } });
    card.addEventListener("keydown", (e) => {
      if (e.target === input || e.metaKey || e.ctrlKey || e.altKey) return;
      if (!graded && /^[1-9]$/.test(e.key) && choiceEls[Number(e.key) - 1]) { e.preventDefault(); choiceEls[Number(e.key) - 1].click(); }
      if (e.key === "Enter" && !graded && e.target.tagName !== "BUTTON") { e.preventDefault(); submit.click(); }
    });
  };
  show();
  return wrap;
}

function showSummary(stage, sess, s) {
  if (!s) { stage.replaceChildren(h("div", { class: "card" }, "Session ended; nothing answered.")); return; }
  const parts = [];
  parts.push(h("div", { class: "row", style: { alignItems: "baseline", gap: "18px" } },
    h("span", { class: "score-big" }, pct(s.score)),
    h("span", { class: "muted" }, `${s.correct} of ${s.total} correct`),
    s.kind === "quiz" ? (s.gated
      ? (s.pass ? h("span", { class: "badge pass" }, "✓ Pass") : h("span", { class: "badge fail" }, `✗ Below the ${pct(sess.threshold)} pass mark`))
      : h("span", { class: "badge" }, "Practice — not recorded for the gate")) : null));
  if (s.gates_passed && s.gates_passed.length) {
    parts.push(h("div", { class: "notice good" }, h("b", {}, "🎉 Gate passed: " + s.gates_passed.join(", ") + ". "), "Modules that depend on it may now be unlocked."));
  }
  if (s.needs_refresh && s.needs_refresh.length) {
    parts.push(h("div", { class: "notice warn" }, h("b", {}, "Needs refresh: " + s.needs_refresh.join(", ") + ". "), "Recent review accuracy dropped below the policy threshold; re-read the unit wrap-ups and pass a full quiz to clear it."));
  }
  const gateQuiz = (s.gate || []).find((c) => c.name === "quiz");
  if (gateQuiz) parts.push(h("p", {}, h("b", {}, "Gate (quiz): "), gateQuiz.detail));
  if (s.missed_tags && s.missed_tags.length) {
    parts.push(h("p", {}, h("b", {}, "Missed by tag: "), s.missed_tags.map((t) => `${t.tag} ×${t.n}`).join(", "), ". Re-read the referenced sections; missed questions come back in review tomorrow."));
  }
  const actions = h("div", { class: "q-actions" });
  if (s.kind === "quiz") actions.append(h("a", { class: "btn primary", href: "/module/" + s.module }, "Back to " + s.module), h("a", { class: "btn", href: "/progress" }, "Progress"));
  else actions.append(h("a", { class: "btn primary", href: "/" }, "Roadmap"), h("a", { class: "btn", href: "/progress" }, "Progress"));
  stage.replaceChildren(h("div", { class: "card qcard" }, h("div", { class: "eyebrow" }, "Results"), ...parts, actions));
  refreshNav();
}

// ---------------------------------------------------------------- estimation trainer

async function viewEstimate() {
  setTitle("Estimation trainer");
  const st = statusCache || (await api("GET", "/api/status"));
  const es = st.report.estimate;
  const pol = st.policy;
  const kinds = ["qps", "storage", "bandwidth", "servers", "cache", "concurrency", "availability"];
  const kindSel = h("select", {}, h("option", { value: "any" }, "Mixed (any kind)"), kinds.map((k) => h("option", { value: k }, k)));
  const nSel = h("select", {}, [3, 5, 10].map((n) => h("option", { value: n, selected: n === 5 }, `${n} problems`)));
  const stage = h("div", {});
  const masteryCard = (es) => h("div", { class: "card" },
    h("div", { class: "eyebrow" }, "Mastery"),
    h("p", { style: { margin: "6px 0" } }, es.mastered
      ? "✓ Mastered at the current window. Keep it warm with a few problems a week."
      : `${es.window}/${pol.estimate_mastery_window} recent drills, ${pct(es.window_rate)} within ±${pct(pol.estimate_tolerance)}. Mastery = ≥ ${pct(pol.estimate_mastery_rate)} over the last ${pol.estimate_mastery_window}, every kind tried.`),
    h("div", { class: "meter" }, h("span", { style: { width: Math.min(100, es.window_rate * 100) + "%" } })),
    h("div", { class: "chips", style: { marginTop: "10px" } }, kinds.map((k) => h("span", { class: "chip" + ((es.kinds_covered || []).includes(k) ? " on" : "") }, ((es.kinds_covered || []).includes(k) ? "✓ " : "") + k))));
  const mcard = h("div", {}, masteryCard(es));
  const start = async () => {
    stage.replaceChildren(h("div", { class: "loading" }, "Generating problems…"));
    try {
      const set = await api("POST", "/api/estimate", { kind: kindSel.value, n: Number(nSel.value) });
      runEstimate(stage, set, (m) => mcard.replaceChildren(masteryCard(m)));
    } catch (e) {
      stage.replaceChildren(errorBox(e));
    }
  };
  return h("div", { class: "narrow" },
    h("div", { class: "page-head" }, h("div", { class: "eyebrow" }, "Practice"), h("h1", {}, "Estimation trainer"),
      h("p", {}, "Randomized back-of-envelope problems. Work each one on paper first, carry units on every line, then type your answer. Every problem ends with the worked solution and the design decision the number drives.")),
    mcard,
    h("div", { class: "card", style: { marginTop: "14px" } },
      h("div", { class: "row", style: { alignItems: "flex-end" } },
        h("label", { class: "field" }, "Kind", kindSel),
        h("label", { class: "field" }, "Set size", nSel),
        h("button", { type: "button", class: "btn primary", onclick: start }, "Start a set"))),
    h("div", { style: { marginTop: "18px" } }, stage));
}

function runEstimate(stage, set, onMastery) {
  let within = 0;
  const show = (p) => {
    const input = h("input", { type: "text", autocomplete: "off", placeholder: "e.g. 1.2k, 40 TB, 12 Gbps, 45 min", "aria-label": "Your estimate" });
    const err = h("div", { class: "predict-msg err", role: "alert" });
    const submit = h("button", { type: "button", class: "btn primary" }, "Check ", h("span", { class: "kbd" }, "↵"));
    const skip = h("button", { type: "button", class: "btn ghost" }, "Show solution");
    const actions = h("div", { class: "q-actions" }, submit, skip);
    const card = h("div", { class: "card qcard" },
      h("div", { class: "q-progress" }, h("span", {}, `Problem ${p.index + 1} of ${set.total}` + (p.index ? ` · ${within} within tolerance` : "")),
        h("span", { class: "q-tags" }, h("span", { class: "tag" }, p.kind), h("span", { class: "tag" }, `±${pct(set.tolerance)}`))),
      h("div", { class: "q-bar" }, h("span", { style: { width: (p.index / set.total) * 100 + "%" } })),
      h("div", { class: "q-prompt" }, p.prompt),
      h("div", { class: "num-input" }, input, h("span", { class: "unit" }, "answer in " + p.unit)),
      err, actions);
    stage.replaceChildren(card);
    input.focus({ preventScroll: true });
    let done = false;
    const send = async (body) => {
      if (done) return;
      err.textContent = "";
      submit.disabled = skip.disabled = true;
      let r;
      try { r = await api("POST", `/api/estimate/${set.id}/answer`, body); }
      catch (e) { err.textContent = e.message; submit.disabled = skip.disabled = false; return; }
      done = true;
      input.disabled = true;
      const res = r.result;
      within = r.within;
      const fb = h("div", { class: "feedback " + (res.within ? "ok" : "no"), role: "status" },
        h("div", { class: "verdict" }, res.skipped ? "Skipped (counts as a miss)" : res.within ? "✓ Within tolerance" : "✗ Outside tolerance"),
        h("div", { class: "meta" }, res.skipped ? `Reference: ${res.expected}` : `You: ${res.given} · Reference: ${res.expected} · ratio ${res.ratio.toFixed(2)}×`),
        res.note ? h("div", { class: "meta" }, res.note) : null,
        res.hint ? h("div", { class: "meta" }, h("b", {}, "Likely slip: "), res.hint) : null,
        h("div", { class: "meta", style: { marginTop: "12px" } }, h("b", {}, "Worked solution")),
        h("ol", { class: "steps" }, res.steps.map((s) => h("li", {}, s))),
        h("div", { class: "sowhat" }, h("b", {}, "So what: "), res.decision));
      card.insertBefore(fb, err);
      if (r.mastery) onMastery(r.mastery);
      const next = h("button", { type: "button", class: "btn primary" }, r.done ? "Finish set " : "Next problem ", h("span", { class: "kbd" }, "↵"));
      next.addEventListener("click", () => {
        if (r.next) show(r.next);
        else {
          stage.replaceChildren(h("div", { class: "card qcard" },
            h("div", { class: "eyebrow" }, "Set complete"),
            h("div", { class: "row", style: { alignItems: "baseline", gap: "16px" } }, h("span", { class: "score-big" }, `${r.within}/${r.count}`), h("span", { class: "muted" }, "within tolerance")),
            set.counted ? null : h("p", { class: "muted" }, "This set used a non-default tolerance, so it doesn't count toward mastery."),
            h("div", { class: "q-actions" }, h("button", { type: "button", class: "btn primary", onclick: () => navigate("/estimate", true) }, "Another set"), h("a", { class: "btn", href: "/progress" }, "Progress"))));
          refreshNav();
        }
      });
      actions.replaceChildren(next);
      next.focus({ preventScroll: true });
    };
    submit.addEventListener("click", () => {
      if (!input.value.trim()) { err.textContent = "Enter your estimate (or Show solution)."; return; }
      send({ input: input.value });
    });
    skip.addEventListener("click", () => send({ skip: true }));
    input.addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); submit.click(); } });
  };
  show(set.problem);
}

// ---------------------------------------------------------------- labs

async function viewLabs(id) {
  const d = await api("GET", "/api/labs/" + encodeURIComponent(id));
  setTitle(d.module + " labs");
  const wrap = h("div", { class: "container" });
  const head = h("div", { class: "page-head" },
    h("div", { class: "crumbs" }, h("a", { href: "/" }, "Roadmap"), " / ", h("a", { href: "/module/" + d.module }, d.module), " / Labs"),
    h("h1", {}, `${d.module} labs`),
    h("p", {}, "Edit the stubs under ", h("code", {}, d.dir || "labs/"), " in your editor, then run the tests here. ",
      d.race_available ? "Tests run with the race detector (-race), like the gate." : "cgo is disabled here, so tests run without -race."));
  wrap.append(head);
  if (!d.written) { wrap.append(h("div", { class: "notice info" }, "This module isn't written yet.")); return wrap; }
  if (!d.packages.length) { wrap.append(h("div", { class: "notice info" }, "This module has no labs.")); return wrap; }

  const gateOut = h("div", {});
  const gateBtn = h("button", { type: "button", class: "btn primary" }, "Run all core labs");
  const gate = h("div", { class: "card" },
    h("div", { class: "row", style: { justifyContent: "space-between" } },
      h("div", {}, h("h3", {}, "Gate run"),
        h("div", { class: "small muted" }, d.gate ? d.gate.detail : "This module's gate has no lab component."),
        h("div", { class: "small muted" }, "Runs every CORE package, like ", h("code", {}, `sd lab ${d.module}`), ", and records the result for the gate.")),
      gateBtn),
    gateOut);
  gateBtn.addEventListener("click", () => runTests(gateBtn, gateOut, `/api/labs/${d.module}/run`, false, () => render()));
  wrap.append(gate, h("h2", { class: "section" }, "Packages"));
  const grid = h("div", { class: "grid", style: { gap: "14px" } });
  for (const p of d.packages) grid.append(labCard(d, p));
  wrap.append(grid);
  if (d.readme) {
    const readme = h("div", { class: "card", style: { marginTop: "26px" } }, h("div", { class: "loading" }, "Loading lab README…"));
    wrap.append(h("h2", { class: "section" }, "Lab guide"), readme);
    api("GET", "/api/doc?path=" + encodeURIComponent(d.readme)).then((doc) => {
      const body = h("article", { class: "prose", html: md.render(doc.markdown) });
      fixLinks(body, doc.path);
      linkify(body);
      addCopyButtons(body);
      readme.replaceChildren(body);
      renderMermaid(body);
    }).catch((e) => readme.replaceChildren(errorBox(e)));
  }
  return wrap;
}

function labCard(d, p) {
  const out = h("div", {});
  const verbose = h("input", { type: "checkbox" });
  const runBtn = h("button", { type: "button", class: "btn primary small" }, "▶ Run tests");
  const solution = h("span", {});
  const setSolution = (pkg) => {
    if (!pkg.has_solution) solution.replaceChildren(h("span", { class: "small muted" }, "No reference solution."));
    else if (pkg.solution_unlocked) solution.replaceChildren(h("a", { class: "btn small", href: `/lab/${d.module}/${pkg.package}/solution` }, "View reference solution"));
    else solution.replaceChildren(h("span", { class: "small muted", title: "Look only after a real attempt" }, "🔒 Solution unlocks after your first test run"));
  };
  setSolution(p);
  const last = h("div", { class: "small" });
  const setLast = (pkg) => {
    last.replaceChildren(pkg.last
      ? h("span", {}, "Last run ", fmtDateTime(pkg.last.at), ": ", pkg.last.passed ? h("span", { class: "badge pass" }, "✓ pass") : h("span", { class: "badge fail" }, `✗ ${pkg.last.failed_tests || ""} failing`.replace("  ", " ")), pkg.last.race ? " (-race)" : "")
      : h("span", { class: "muted" }, "Not run yet."));
  };
  setLast(p);
  runBtn.addEventListener("click", () => runTests(runBtn, out, `/api/labs/${d.module}/${p.package}/run`, verbose.checked, (r) => {
    const pkg = r.labs.packages.find((x) => x.package === p.package);
    if (pkg) { setSolution(pkg); setLast(pkg); }
  }));
  return h("div", { class: "card lab-card" },
    h("div", { class: "row", style: { justifyContent: "space-between" } },
      h("div", {}, h("h3", { style: { margin: 0 } }, h("code", {}, p.package), " — ", p.title),
        h("div", { class: "small muted" }, `Unit ${p.unit || "?"} · `, h("code", {}, p.dir))),
      h("span", { class: "badge " + (p.core ? "core" : "stretch") }, p.core ? "Core" : "Stretch")),
    h("div", { class: "lab-files" }, p.files.map((f) => h("code", { title: "Open in your editor: " + p.dir + "/" + f }, f))),
    last,
    h("div", { class: "row" }, runBtn, h("label", { class: "small muted row", style: { gap: "6px" } }, verbose, "verbose (-v)"), h("span", { class: "spacer" }), solution),
    out);
}

async function runTests(btn, out, url, verbose, after) {
  btn.disabled = true;
  const started = Date.now();
  const timer = h("span", { class: "small muted" }, "0s");
  out.replaceChildren(h("div", { class: "row", style: { marginTop: "8px" } }, h("span", { class: "spinner" }), h("span", { class: "small" }, "Running go test… the first -race build can take a minute."), timer));
  const tick = setInterval(() => { timer.textContent = Math.round((Date.now() - started) / 1000) + "s"; }, 500);
  try {
    const r = await api("POST", url, { verbose });
    const verdict = r.passed
      ? h("div", { class: "notice good" }, `✓ Passing (${r.seconds.toFixed(1)}s${r.race ? ", -race" : ""}).`)
      : h("div", { class: "notice bad" }, `✗ Not passing yet (${r.seconds.toFixed(1)}s). The failure messages say what behaviour is expected and why.`);
    const gates = r.gates_passed && r.gates_passed.length ? h("div", { class: "notice good" }, "🎉 Gate passed: " + r.gates_passed.join(", ")) : null;
    out.replaceChildren(...[verdict, gates, testOutput(r.output || "(no output)")].filter(Boolean));
    if (after) after(r);
  } catch (e) {
    out.replaceChildren(errorBox(e));
  } finally {
    clearInterval(tick);
    btn.disabled = false;
  }
}

function testOutput(text) {
  const pre = h("pre", { class: "output" });
  for (const line of text.split("\n")) {
    let cls = "";
    if (/^(--- FAIL|FAIL|panic:|\s+\S+_test\.go:\d+:)/.test(line) || /WARNING: DATA RACE/.test(line)) cls = "fail";
    else if (/^(ok\s|PASS|--- PASS)/.test(line)) cls = "pass";
    else if (/^(=== |\s*$)/.test(line)) cls = "dim";
    pre.append(cls ? h("span", { class: cls }, line) : line, "\n");
  }
  return pre;
}

async function viewSolution(id, pkg) {
  const d = await api("GET", `/api/labs/${encodeURIComponent(id)}/${encodeURIComponent(pkg)}/solution`);
  setTitle(`${d.module}/${d.package} solution`);
  const files = d.files.filter((f) => !f.test).map((f) => {
    const code = hljs.highlight(f.content, { language: "go", ignoreIllegals: true }).value;
    const pre = h("pre", {}, h("code", { class: "hljs language-go", html: code }));
    return h("div", { class: "codefile" }, h("h3", {}, d.dir + "/" + f.name), pre);
  });
  const tests = d.files.filter((f) => f.test).map((f) => f.name);
  const view = h("div", { class: "container" },
    h("div", { class: "page-head" },
      h("div", { class: "crumbs" }, h("a", { href: "/" }, "Roadmap"), " / ", h("a", { href: "/module/" + d.module }, d.module), " / ", h("a", { href: "/lab/" + d.module }, "Labs"), " / Solution"),
      h("h1", {}, `Reference solution: ${d.package}`)),
    h("div", { class: "notice warn" }, "Compare approaches; don't copy. Never paste a solution into ", h("code", {}, "labs/"), " — the point is the version you wrote."),
    files,
    tests.length ? h("p", { class: "muted small" }, "Tests (identical to your lab's): ", tests.join(", ")) : null);
  addCopyButtons(view);
  return view;
}

// ---------------------------------------------------------------- progress dashboard

async function viewProgress() {
  const d = await api("GET", "/api/progress");
  setTitle("Progress");
  const rep = d.report;
  const thr = d.policy.quiz_pass_threshold;
  const core = rep.modules.filter((m) => m.core);
  const passedCore = core.filter((m) => m.gate_passed_at).length;

  const wrap = h("div", { class: "container" },
    h("div", { class: "page-head" }, h("div", { class: "eyebrow" }, "Dashboard"), h("h1", {}, "Progress"),
      h("p", {}, d.started_at ? `Started ${fmtDate(d.started_at)}. ` : "", "Everything here is read from progress.json, which the CLI shares.")),
    h("div", { class: "grid tiles" },
      tile("Core gates passed", `${passedCore}/${core.length}`, "modules", passedCore / Math.max(core.length, 1)),
      tile("Quiz attempts", String(d.quiz_history.length), d.quiz_history.length ? `${d.quiz_history.filter((q) => q.passed).length} at ≥ ${pct(thr)}` : "none yet"),
      tile("Review accuracy", d.review.recent ? pct(d.review.accuracy_recent) : "—", d.review.recent ? `last ${d.review.recent} reviews` : "no reviews yet", d.review.recent ? d.review.accuracy_recent : null),
      tile("Estimation", rep.estimate.mastered ? "Mastered" : pct(rep.estimate.window_rate), `${rep.estimate.window} recent drills`, rep.estimate.window_rate)));

  // Gate checklists
  wrap.append(h("h2", { class: "section" }, "Gate checklist by module"));
  if (!d.modules.length) wrap.append(h("p", { class: "empty" }, "No module activity yet. Open A0 or B0 on the roadmap to start."));
  const gates = h("div", { class: "grid two", style: { alignItems: "start" } });
  for (const m of d.modules) {
    gates.append(h("div", { class: "card" },
      h("div", { class: "row", style: { justifyContent: "space-between" } }, h("h3", { style: { margin: 0 } }, h("a", { href: "/module/" + m.id }, m.id), " ", m.title), stateBadge(m)),
      h("ul", { class: "checklist", style: { marginTop: "8px" } }, (m.components || []).map((c) => h("li", { style: { gridTemplateColumns: "26px 1fr" } },
        h("span", { class: "check" + (c.met ? " met" : "") }, c.met ? "✓" : ""),
        h("div", {}, h("div", { class: "check-name" }, compName(c.name)), h("div", { class: "check-detail" }, c.detail))))),
      m.predictions ? h("div", { class: "small muted" }, `${m.predictions} lesson predictions written.`) : null,
      m.designs.length ? h("div", { class: "small muted" }, "Latest design notes: ", m.designs[m.designs.length - 1].notes || "—") : null));
  }
  wrap.append(gates);

  // Quiz history
  wrap.append(h("h2", { class: "section" }, "Quiz history"));
  if (!d.quiz_history.length) wrap.append(h("p", { class: "empty" }, "No gated quiz attempts yet."));
  else wrap.append(h("div", { class: "card" }, quizChart(d.quiz_history, thr), attemptsTableWithModule(d.quiz_history.slice(-15), thr)));

  // Review queue
  const rv = d.review;
  wrap.append(h("h2", { class: "section" }, "Spaced-review queue"));
  const rq = h("div", { class: "card" });
  rq.append(h("div", { class: "row", style: { justifyContent: "space-between" } },
    h("div", {}, h("b", {}, `${rv.due_today} due today`), h("span", { class: "muted" }, ` · ${rv.total} cards in the deck · ${rv.lapses} lapses`)),
    rv.due_today ? h("a", { class: "btn primary", href: "/review" }, "Start review") : null));
  if (rv.total) {
    rq.append(h("h3", { class: "sub" }, "Cards by Leitner box (review interval)"),
      h("div", { class: "boxes" }, rv.boxes.map((n, b) => h("div", { class: "box" }, h("div", { class: "n" }, String(n)), h("div", { class: "d" }, `${rv.intervals[b]}d`)))));
  }
  const queueTable = (rows, label) => rows.length ? [h("h3", { class: "sub" }, label), h("div", { class: "table-wrap" }, h("table", { class: "data" },
    h("thead", {}, h("tr", {}, h("th", {}, "Due"), h("th", {}, "Card"), h("th", {}, "Question"), h("th", { class: "num" }, "Box"))),
    h("tbody", {}, rows.map((c) => h("tr", {}, h("td", {}, c.due), h("td", {}, h("a", { href: "/module/" + c.module }, c.qid)), h("td", {}, c.prompt), h("td", { class: "num" }, String(c.box)))))))] : [];
  rq.append(...queueTable(rv.due, "Due now"), ...queueTable(rv.upcoming, "Coming up"));
  if (!rv.total) rq.append(h("p", { class: "empty" }, "Your deck is empty: questions join it when you take a quiz."));
  wrap.append(rq);

  // Weak topics + estimation by kind
  const weak = h("div", { class: "card" }, h("h3", {}, "Weakest tags"), h("p", { class: "small muted", style: { marginTop: 0 } }, "Accuracy over each tag's last 20 answers (quizzes and reviews), weakest first."));
  if (!d.weak_tags.length) weak.append(h("p", { class: "empty" }, "Needs at least 3 answers per tag."));
  else weak.append(bars(d.weak_tags.slice(0, 12).map((t) => ({ name: t.tag, frac: t.accuracy, label: `${pct(t.accuracy)} · ${t.correct}/${t.total}` }))));
  const est = h("div", { class: "card" }, h("h3", {}, "Estimation by kind"), h("p", { class: "small muted", style: { marginTop: 0 } }, `Share of drills within ±${pct(d.policy.estimate_tolerance)}, all time.`));
  const kinds = d.estimate_kinds.map((k) => { const v = d.estimate_by_kind[k] || [0, 0]; return { name: k, frac: v[1] ? v[0] / v[1] : null, label: v[1] ? `${pct(v[0] / v[1])} · ${v[0]}/${v[1]}` : "—" }; });
  est.append(bars(kinds), h("div", { class: "q-actions" }, h("a", { class: "btn small", href: "/estimate" }, "Practise")));
  wrap.append(h("h2", { class: "section" }, "Weak topics"), h("div", { class: "grid two", style: { alignItems: "start" } }, weak, est));

  // Notes and activity
  const notes = h("div", { class: "card" }, h("h3", {}, "Tutor notes"));
  if (!d.tutor_notes.length) notes.append(h("p", { class: "empty" }, "None yet."));
  for (const n of d.tutor_notes) notes.append(h("p", { class: "small" }, h("b", {}, fmtDate(n.at)), n.module ? ` · ${n.module}` : "", (n.tags || []).length ? ` [${n.tags.join(", ")}]` : "", " — ", n.note));
  const act = h("div", { class: "card" }, h("h3", {}, "Recent activity"));
  if (!d.activity.length) act.append(h("p", { class: "empty" }, "Nothing yet."));
  else act.append(h("div", { class: "table-wrap" }, h("table", { class: "data" }, h("tbody", {}, d.activity.map((a) => h("tr", {},
    h("td", { class: "muted" }, fmtDateTime(a.at)), h("td", {}, a.kind.replace("_", " ")), h("td", {}, a.module ? h("a", { href: "/module/" + a.module }, a.module) : ""), h("td", {}, a.detail || "")))))));
  wrap.append(h("h2", { class: "section" }, "Notes & activity"), h("div", { class: "grid two", style: { alignItems: "start" } }, notes, act));
  return wrap;
}

function bars(rows) {
  return h("div", { class: "bars" }, rows.map((r) => h("div", { class: "bar-row" },
    h("span", { class: "name", title: r.name }, r.name),
    h("span", { class: "track-bg" }, r.frac == null ? null : h("span", { class: "fill", style: { width: Math.max(1, r.frac * 100) + "%" } })),
    h("span", { class: "val" }, r.label))));
}

function attemptsTableWithModule(rows, thr) {
  return h("div", { class: "table-wrap", style: { marginTop: "16px" } }, h("table", { class: "data" },
    h("thead", {}, h("tr", {}, h("th", {}, "When"), h("th", {}, "Module"), h("th", { class: "num" }, "Score"), h("th", {}, "Result"))),
    h("tbody", {}, rows.slice().reverse().map((a) => h("tr", {},
      h("td", {}, fmtDateTime(a.at)), h("td", {}, h("a", { href: "/module/" + a.module }, a.module)),
      h("td", { class: "num" }, `${a.correct}/${a.total} · ${pct(a.score)}`),
      h("td", {}, a.passed ? h("span", { class: "badge pass" }, "✓ Pass") : h("span", { class: "badge fail" }, "✗ Below")))))));
}

let tip = null;
function showTip(e, text) {
  if (!tip) { tip = h("div", { class: "tooltip", role: "tooltip" }); document.body.append(tip); }
  tip.textContent = text;
  tip.hidden = false;
  const x = Math.min(e.clientX + 12, window.innerWidth - tip.offsetWidth - 8);
  tip.style.left = x + "px";
  tip.style.top = e.clientY - 36 + "px";
}
function hideTip() { if (tip) tip.hidden = true; }

function quizChart(rows, thr) {
  const recent = rows.slice(-40);
  const chart = h("div", { class: "hist", role: "img", "aria-label": `Quiz scores, last ${recent.length} attempts; pass mark ${pct(thr)}` });
  for (const v of [0, 0.5, 1]) chart.append(h("span", { class: "axis", style: { bottom: v * 100 + "%" } }, pct(v)));
  for (const a of recent) {
    const text = `${a.module} · ${fmtDate(a.at)} · ${a.correct}/${a.total} (${pct(a.score)}) ${a.passed ? "✓ pass" : "✗ below"}`;
    chart.append(h("div", {
      class: "col " + (a.passed ? "pass" : "fail"), tabindex: "0", "aria-label": text,
      onmousemove: (e) => showTip(e, text), onmouseleave: hideTip,
      onfocus: (e) => { const r = e.target.getBoundingClientRect(); showTip({ clientX: r.left, clientY: r.top }, text); }, onblur: hideTip,
    }, h("span", { style: { height: Math.max(2, a.score * 100) + "%" } })));
  }
  chart.append(h("div", { class: "thresh", style: { bottom: thr * 100 + "%" } }, h("b", {}, `pass ${pct(thr)}`)));
  return h("div", {}, h("p", { class: "small muted", style: { marginTop: 0 } }, `Score per gated attempt, oldest → newest${rows.length > 40 ? " (last 40)" : ""}. Faded bars are below the pass mark.`), chart);
}

// ---------------------------------------------------------------- boot

applyTheme();
render();
