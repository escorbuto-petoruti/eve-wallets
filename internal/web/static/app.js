// EVE wallets front end. Plain JavaScript, no build step.
// All server text is inserted with textContent, never as HTML.
(function () {
  "use strict";

  var PALETTE = ["#61cce5", "#8ed7bc", "#f0b36b", "#b69cff", "#ff8a8a",
    "#5aa9ff", "#f28dc4", "#c3e07a", "#ff9f6e", "#9aa7b4"];
  var TOTAL_COLOR = "#e9f0f6";
  var MAX_IDS = 50; // keep in sync with the server cap

  var state = { wallets: [], range: 2592000, sections: [], active: null };

  var $ = function (id) { return document.getElementById(id); };

  function el(tag, text, cls) {
    var n = document.createElement(tag);
    if (text !== undefined) { n.textContent = text; }
    if (cls) { n.className = cls; }
    return n;
  }

  // eveImage returns a decorative <img> for a character portrait or a
  // corporation logo, or null when the id is not a positive safe integer.
  // The URL is built only from that number. A failed load removes the element.
  function eveImage(kind, id, px) {
    if (!Number.isSafeInteger(id) || !(id > 0)) { return null; }
    var url = kind === "character"
      ? "https://images.evetech.net/characters/" + id + "/portrait?size=64"
      : "https://images.evetech.net/corporations/" + id + "/logo?size=64";
    var img = document.createElement("img");
    img.className = "eve-img";
    img.setAttribute("alt", "");
    img.setAttribute("width", String(px));
    img.setAttribute("height", String(px));
    img.setAttribute("loading", "lazy");
    img.setAttribute("decoding", "async");
    img.src = url;
    img.addEventListener("error", function () { img.remove(); });
    return img;
  }

  var POLL_FIRST_MS = 5000;
  var POLL_MAX_MS = 30000;
  var POLL_MAX_TRIES = 40;
  var EXPIRED_MSG = "Your session expired. Sign in again.";

  // epoch changes on every sign-out so late responses of an old view are dropped.
  var session = { signedIn: false, epoch: 0, pollTimer: null };

  // getJSON rejects with err.unauthorized for a 401; the page has already
  // switched to the signed-out view by then, callers just stay quiet.
  function getJSON(url) {
    var epoch = session.epoch;
    return fetch(url, { headers: { "Accept": "application/json" } }).then(function (r) {
      return r.json().catch(function () { return null; }).then(function (body) {
        if (r.status === 401) {
          var wasSignedIn = session.signedIn;
          if (epoch === session.epoch) { showSignedOut(wasSignedIn ? EXPIRED_MSG : ""); }
          var e = new Error("not signed in");
          e.unauthorized = true;
          throw e;
        }
        if (epoch !== session.epoch) { var s = new Error("stale"); s.unauthorized = true; throw s; }
        if (!r.ok) { throw new Error(body && body.error ? body.error : "request failed (" + r.status + ")"); }
        return body;
      });
    });
  }

  function stopPolling() {
    if (session.pollTimer !== null) { clearTimeout(session.pollTimer); session.pollTimer = null; }
  }

  function showSignedOut(message) {
    session.signedIn = false;
    session.epoch++;
    stopPolling();
    clearSections();
    $("signed-in").hidden = true;
    $("user-bar").hidden = true;
    $("session-message").textContent = message || "";
    $("signed-out").hidden = false;
  }

  function showSignedIn(me) {
    session.signedIn = true;
    session.epoch++;
    $("signed-out").hidden = true;
    $("user-name").textContent = me.name;
    $("characters").textContent = (me.characters || []).map(function (c) { return c.name; }).join(", ");
    $("user-bar").hidden = false;
    $("signed-in").hidden = false;
  }

  var iskFmt = new Intl.NumberFormat(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
  function formatISK(cents) { return iskFmt.format(cents / 100); }
  function formatTime(unix) { return new Date(unix * 1000).toLocaleString(); }

  // walletLabel is the wallet name. Wallets are always shown inside their
  // owner's tab, so the owner name is not repeated.
  function walletLabel(w) {
    return w.name || (w.kind === "character" ? w.owner_name : w.division === 1 ? "Master Wallet" : "Division " + w.division);
  }
  function ownerLabel(w) {
    return w.owner_name + " (" + (w.kind === "character" ? "character" : "corporation") + ")";
  }

  // groupWallets returns one group for all characters and one per corporation
  // (by owner_id). Groups only exist when they hold wallets.
  function groupWallets(wallets) {
    var chars = [];
    var corps = new Map();
    wallets.forEach(function (w) {
      if (w.kind === "character") { chars.push(w); return; }
      if (!corps.has(w.owner_id)) { corps.set(w.owner_id, { key: "corp-" + w.owner_id, title: w.owner_name, corpId: w.owner_id, wallets: [] }); }
      corps.get(w.owner_id).wallets.push(w);
    });
    var groups = [];
    if (chars.length) { groups.push({ key: "characters", title: "Characters", wallets: chars }); }
    corps.forEach(function (g) { groups.push(g); });
    return groups;
  }

  function destroyCharts(section) {
    section.charts.forEach(function (c) { c.destroy(); });
    section.charts = [];
  }

  function clearSections() {
    state.sections.forEach(function (s) {
      s.token++; // drop late responses
      destroyCharts(s);
    });
    state.sections = [];
    state.active = null;
    $("tabs").replaceChildren();
    $("tabs").hidden = true;
    $("sections").replaceChildren();
  }

  function buildSection(group, idx) {
    var section = { group: group, charts: [], token: 0, note: null, grid: null, tab: null, panel: null, drawn: false };
    var card = el("section", undefined, "card");
    card.id = "panel-" + idx;
    card.setAttribute("role", "tabpanel");
    card.setAttribute("aria-labelledby", "tab-" + idx);
    card.hidden = true;
    section.panel = card;
    var tab = el("button", group.title, "tab");
    tab.type = "button";
    tab.id = "tab-" + idx;
    tab.setAttribute("role", "tab");
    tab.setAttribute("aria-controls", card.id);
    tab.setAttribute("aria-selected", "false");
    tab.tabIndex = -1;
    tab.addEventListener("click", function () { activateSection(section); });
    section.tab = tab;
    var tabLogo = eveImage("corporation", group.corpId, 24);
    if (tabLogo) { tab.insertBefore(tabLogo, tab.firstChild); }
    $("tabs").appendChild(tab);
    var title = el("h2", group.title);
    var titleLogo = eveImage("corporation", group.corpId, 32);
    if (titleLogo) { title.insertBefore(titleLogo, title.firstChild); }
    card.appendChild(title);

    section.note = el("p", "", "muted");
    section.note.setAttribute("role", "status");
    card.appendChild(section.note);
    section.grid = el("div", undefined, "panels");
    card.appendChild(section.grid);

    card.appendChild(el("h3", "Latest balances"));
    var scroll = el("div", undefined, "scroll");
    var table = document.createElement("table");
    var head = document.createElement("tr");
    [["Wallet"], ["Balance (ISK)", "num"], ["As of"]].forEach(function (c) { head.appendChild(el("th", c[0], c[1])); });
    var thead = document.createElement("thead");
    thead.appendChild(head);
    table.appendChild(thead);
    var tbody = document.createElement("tbody");
    group.wallets.forEach(function (w) {
      if (w.cents === null || w.cents === undefined) { return; }
      var tr = document.createElement("tr");
      var nameCell = el("td");
      var rowPic = w.kind === "character" ? eveImage("character", w.owner_id, 32) : null;
      if (rowPic) { nameCell.appendChild(rowPic); }
      nameCell.appendChild(document.createTextNode(" " + walletLabel(w)));
      tr.appendChild(nameCell);
      tr.appendChild(el("td", formatISK(w.cents), "num"));
      tr.appendChild(el("td", formatTime(w.balance_time)));
      tbody.appendChild(tr);
    });
    table.appendChild(tbody);
    scroll.appendChild(table);
    card.appendChild(scroll);

    $("sections").appendChild(card);
    return section;
  }

  // activateSection shows one panel. Only the active tab keeps a chart: the
  // others are destroyed and their pending requests are invalidated.
  function activateSection(section) {
    if (state.active === section && section.drawn) { return; }
    state.sections.forEach(function (s) {
      var on = s === section;
      if (!on) {
        s.token++;
        destroyCharts(s);
        s.drawn = false;
      }
      s.panel.hidden = !on;
      s.tab.setAttribute("aria-selected", on ? "true" : "false");
      s.tab.tabIndex = on ? 0 : -1;
    });
    state.active = section;
    refreshChart(section);
  }

  function onTabKey(ev) {
    var n = state.sections.length;
    var i = state.sections.findIndex(function (s) { return s.tab === ev.target; });
    if (i < 0 || n === 0) { return; }
    var next;
    if (ev.key === "ArrowRight") { next = (i + 1) % n; }
    else if (ev.key === "ArrowLeft") { next = (i + n - 1) % n; }
    else if (ev.key === "Home") { next = 0; }
    else if (ev.key === "End") { next = n - 1; }
    else { return; }
    ev.preventDefault();
    activateSection(state.sections[next]);
    state.sections[next].tab.focus();
  }

  // The selected tab survives a reload while its owner still exists.
  function renderSections() {
    var prev = state.active ? state.active.group.key : null;
    clearSections();
    groupWallets(state.wallets).forEach(function (g, i) {
      state.sections.push(buildSection(g, i));
    });
    if (!state.sections.length) { return; }
    var pick = state.sections.find(function (s) { return s.group.key === prev; }) || state.sections[0];
    $("tabs").hidden = state.sections.length < 2;
    activateSection(pick);
  }

  function renderStatus(s) {
    var box = $("status");
    box.replaceChildren();
    if (!s.taken_at) {
      box.appendChild(el("p", "No collection has run since the server started.", "muted"));
      return;
    }
    box.appendChild(el("p", "Collected " + formatTime(s.taken_at) + ": " + s.snapshots + " balance(s) recorded."));
    if (s.rate_limited) {
      var msg = "ESI rate limit reached; the collection is partial.";
      if (s.retry_after_seconds > 0) { msg += " Retry in about " + s.retry_after_seconds + " s."; }
      box.appendChild(el("p", msg, "warn"));
    }
    function list(title, items, cls) {
      if (!items.length) { return; }
      box.appendChild(el("p", title, cls));
      var ul = el("ul");
      items.forEach(function (t) { ul.appendChild(el("li", t)); });
      box.appendChild(ul);
    }
    var reauth = s.reauth || [];
    reauth.forEach(function (r) {
      var note = el("div", undefined, "warn reauth-notice");
      note.setAttribute("role", "alert");
      note.appendChild(el("p", r.name + ": sign in again", "warn"));
      var link = el("a", "Sign in again", "button");
      link.href = "/auth/add-character";
      note.appendChild(link);
      note.appendChild(el("p", "On the EVE login screen, choose " + r.name + ".", "muted"));
      box.appendChild(note);
    });
    // The reauth characters already have their own notice above.
    var errors = s.errors.filter(function (t) {
      return !reauth.some(function (r) { return t.indexOf("character " + r.character_id + " (") !== -1; });
    });
    list("Skipped:", s.skipped.map(function (k) { return k.owner + ": " + k.reason; }), "warn");
    list("Errors:", errors, "bad");
  }

  function themeColors() {
    var cs = getComputedStyle(document.body);
    return { text: cs.color, grid: cs.getPropertyValue("--border").trim() };
  }

  function totalColor() {
    return getComputedStyle(document.body).color || TOTAL_COLOR;
  }

  function pointsOf(list) {
    return list.map(function (p) { return { x: p.t * 1000, y: p.cents / 100 }; });
  }

  // deltaInfo compares the first and last point of the range. The text label
  // and the sign carry the direction, the color is only a reinforcement.
  function deltaInfo(points) {
    if (points.length < 2) { return { text: "No change data in this range", cls: "muted" }; }
    var first = points[0].cents;
    var diff = points[points.length - 1].cents - first;
    var label = diff > 0 ? "Up" : diff < 0 ? "Down" : "Flat";
    var cls = diff > 0 ? "good" : diff < 0 ? "bad" : "muted";
    var sign = diff > 0 ? "+" : "";
    var pct = first === 0 ? "n/a" : sign + (diff / Math.abs(first) * 100).toFixed(1) + "%";
    return { text: label + " " + sign + formatISK(diff) + " ISK (" + pct + ") over the range", cls: cls };
  }

  // buildPanel adds one small multiple: title, current balance, delta and a
  // canvas with its own Y scale.
  function buildPanel(section, opts) {
    var panel = el("article", undefined, "panel " + opts.cls);
    var h = el("h3", undefined, "panel-title");
    if (opts.image) { h.appendChild(opts.image); }
    h.appendChild(document.createTextNode(opts.label));
    panel.appendChild(h);
    var last = opts.points.length ? opts.points[opts.points.length - 1].cents : opts.cents;
    var hasBalance = last !== null && last !== undefined;
    panel.appendChild(el("p", hasBalance ? formatISK(last) + " ISK" : "No balance yet", "big num"));
    var d = deltaInfo(opts.points);
    panel.appendChild(el("p", d.text, "delta " + d.cls));
    var box = el("div", undefined, "spark");
    var canvas = document.createElement("canvas");
    canvas.setAttribute("role", "img");
    canvas.setAttribute("aria-label", "Balance history for " + opts.label + ": " + (hasBalance ? formatISK(last) + " ISK now. " : "") + d.text);
    box.appendChild(canvas);
    panel.appendChild(box);
    section.grid.appendChild(panel);
    if (opts.points.length) { drawSpark(section, canvas, opts); }
    else { box.replaceChildren(el("p", "No data points in this range.", "muted")); }
  }

  function drawSpark(section, canvas, opts) {
    var c = themeColors();
    section.charts.push(new Chart(canvas, {
      type: "line",
      data: { datasets: [{
        label: opts.label,
        data: pointsOf(opts.points),
        borderColor: opts.color, backgroundColor: opts.color,
        borderWidth: 2, pointRadius: 0, pointHoverRadius: 3, tension: 0, stepped: "before",
        borderDash: opts.dashed ? [6, 4] : []
      }] },
      options: {
        responsive: true, maintainAspectRatio: false, animation: false,
        interaction: { mode: "index", intersect: false },
        parsing: false, normalized: true,
        scales: {
          x: { type: "linear", ticks: { color: c.text, maxTicksLimit: 4, callback: function (v) { return new Date(v).toLocaleDateString(); } }, grid: { color: c.grid } },
          y: { ticks: { color: c.text, maxTicksLimit: 4, callback: function (v) { return iskFmt.format(v); } }, grid: { color: c.grid } }
        },
        plugins: {
          legend: { display: false },
          tooltip: {
            callbacks: {
              title: function (items) { return items.length ? new Date(items[0].parsed.x).toLocaleString() : ""; },
              label: function (item) { return iskFmt.format(item.parsed.y) + " ISK"; }
            }
          }
        }
      }
    }));
  }

  // drawPanels rebuilds the grid: one panel per wallet (colored by its fixed
  // index in the owner group) plus the Total panel.
  function drawPanels(section, resp) {
    destroyCharts(section);
    section.grid.replaceChildren();
    var wallets = section.group.wallets;
    var byId = new Map();
    resp.series.forEach(function (s) { byId.set(s.wallet_id, s.points); });
    wallets.slice(0, MAX_IDS).forEach(function (w, i) {
      buildPanel(section, {
        label: walletLabel(w), cls: "c" + (i % PALETTE.length), color: PALETTE[i % PALETTE.length],
        image: w.kind === "character" ? eveImage("character", w.owner_id, 24) : null,
        points: byId.get(w.id) || [], cents: w.cents
      });
    });
    if (resp.total) {
      buildPanel(section, { label: "Total", cls: "total", color: totalColor(), dashed: true, image: null, points: resp.total, cents: null });
    }
    section.drawn = true;
  }

  function refreshChart(section) {
    var wallets = section.group.wallets;
    var ids = wallets.slice(0, MAX_IDS).map(function (w) { return w.id; });
    var note = section.note;
    note.textContent = "";
    if (wallets.length > MAX_IDS) {
      note.textContent = "Showing the first " + MAX_IDS + " of " + wallets.length + " wallets.";
    }
    var q = new URLSearchParams();
    q.set("wallet_ids", ids.join(","));
    if (state.range > 0) {
      q.set("from", new Date(Date.now() - state.range * 1000).toISOString());
    }
    q.set("total", "1");
    var token = ++section.token;
    getJSON("/api/series?" + q.toString()).then(function (resp) {
      if (token !== section.token) { return; }
      drawPanels(section, resp);
    }).catch(function (err) {
      if (!err.unauthorized && token === section.token) { note.textContent = err.message; }
    });
  }

  function setRange(btn) {
    state.range = Number(btn.getAttribute("data-range"));
    document.querySelectorAll("#ranges button").forEach(function (b) {
      var on = b === btn;
      b.classList.toggle("active", on);
      b.setAttribute("aria-pressed", on ? "true" : "false");
    });
    if (state.active) { refreshChart(state.active); }
  }

  function init() {
    $("tabs").addEventListener("keydown", onTabKey);
    document.querySelectorAll("#ranges button").forEach(function (b) {
      b.addEventListener("click", function () { setRange(b); });
    });

    getJSON("/api/me").then(function (me) {
      showSignedIn(me);
      loadWallets(0, POLL_FIRST_MS);
    }).catch(function (err) {
      if (err.unauthorized) { return; } // the signed-out view is already shown
      $("signed-out").hidden = false;
      $("session-message").textContent = "Cannot check your session: " + err.message;
    });
  }

  function loadStatus() {
    return getJSON("/api/status").then(function (s) { renderStatus(s); return s; }).catch(function (err) {
      if (!err.unauthorized) { $("status").replaceChildren(el("p", err.message, "bad")); }
      return null;
    });
  }

  // loadWallets shows the charts once data exists. Right after a sign-in the
  // first collection may still be running, so it polls with a capped backoff.
  function loadWallets(tries, delay) {
    var epoch = session.epoch;
    Promise.all([getJSON("/api/wallets"), loadStatus()]).then(function (res) {
      var resp = res[0];
      var status = res[1];
      state.wallets = resp.wallets;
      var hasData = state.wallets.some(function (w) { return w.cents !== null && w.cents !== undefined; });
      if (hasData) {
        $("collecting").hidden = true;
        $("empty").hidden = true;
        $("controls").hidden = false;
        renderSections();
        return;
      }
      $("collecting").hidden = false;
      var text = "Collecting your wallets… this can take a minute.";
      if (status && status.errors && status.errors.length) {
        text = "No balances yet. The last collection reported problems (see below).";
      } else if (status && status.skipped && status.skipped.length) {
        text = "No balances yet. Some wallets were skipped (see below).";
      }
      if (tries + 1 >= POLL_MAX_TRIES) {
        text = "Still no balances. Reload the page to check again.";
        $("collecting-text").textContent = text;
        return;
      }
      $("collecting-text").textContent = text;
      session.pollTimer = setTimeout(function () {
        session.pollTimer = null;
        if (epoch === session.epoch && session.signedIn) {
          loadWallets(tries + 1, Math.min(Math.round(delay * 1.5), POLL_MAX_MS));
        }
      }, delay);
    }).catch(function (err) {
      if (err.unauthorized) { return; }
      $("collecting").hidden = true;
      $("empty").hidden = false;
      $("empty-text").textContent = err.message;
    });
  }

  init();
})();
