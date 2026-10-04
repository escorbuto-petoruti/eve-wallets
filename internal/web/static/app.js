// EVE wallets front end. Plain JavaScript, no build step.
// All server text is inserted with textContent, never as HTML.
(function () {
  "use strict";

  var PALETTE = ["#2563eb", "#dc2626", "#16a34a", "#d97706", "#7c3aed",
    "#0891b2", "#db2777", "#65a30d", "#ea580c", "#4b5563"];
  var TOTAL_COLOR = "#e5e5e5";
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

  function clearSections() {
    state.sections.forEach(function (s) {
      s.token++; // drop late responses
      if (s.chart) { s.chart.destroy(); s.chart = null; }
    });
    state.sections = [];
    state.active = null;
    $("tabs").replaceChildren();
    $("tabs").hidden = true;
    $("sections").replaceChildren();
  }

  function buildSection(group, idx) {
    var section = { group: group, selected: new Set(), chart: null, token: 0, totalBox: null, note: null, canvas: null, tab: null, panel: null };
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

    var fs = el("fieldset");
    fs.appendChild(el("legend", "Wallets in " + group.title, "sr-only"));
    var box = el("div", undefined, "wallets");
    group.wallets.slice(0, MAX_IDS).forEach(function (w) { section.selected.add(w.id); });
    group.wallets.forEach(function (w) {
      var label = el("label", undefined, "check");
      var cb = document.createElement("input");
      cb.type = "checkbox";
      cb.checked = section.selected.has(w.id);
      cb.addEventListener("change", function () {
        if (cb.checked) { section.selected.add(w.id); } else { section.selected.delete(w.id); }
        refreshChart(section);
      });
      label.appendChild(cb);
      var pic = w.kind === "character" ? eveImage("character", w.owner_id, 32) : null;
      if (pic) { label.appendChild(pic); }
      label.appendChild(document.createTextNode(" " + walletLabel(w)));
      box.appendChild(label);
    });
    fs.appendChild(box);
    card.appendChild(fs);

    var totalLabel = el("label", undefined, "check");
    section.totalBox = document.createElement("input");
    section.totalBox.type = "checkbox";
    section.totalBox.setAttribute("aria-label", "Total for " + group.title);
    section.totalBox.addEventListener("change", function () { refreshChart(section); });
    totalLabel.appendChild(section.totalBox);
    totalLabel.appendChild(document.createTextNode(" Total"));
    card.appendChild(totalLabel);

    section.note = el("p", "", "muted");
    section.note.setAttribute("role", "status");
    card.appendChild(section.note);
    var chartBox = el("div", undefined, "chart-box");
    section.canvas = document.createElement("canvas");
    section.canvas.setAttribute("role", "img");
    section.canvas.setAttribute("aria-label", "Balance history for " + group.title);
    chartBox.appendChild(section.canvas);
    card.appendChild(chartBox);

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
    if (state.active === section && section.chart) { return; }
    state.sections.forEach(function (s) {
      var on = s === section;
      if (!on) {
        s.token++;
        if (s.chart) { s.chart.destroy(); s.chart = null; }
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

  function drawChart(section, resp) {
    var datasets = resp.series.map(function (s, i) {
      var w = section.group.wallets.find(function (x) { return x.id === s.wallet_id; });
      return {
        label: w ? walletLabel(w) : "Wallet " + s.wallet_id,
        data: s.points.map(function (p) { return { x: p.t * 1000, y: p.cents / 100 }; }),
        borderColor: PALETTE[i % PALETTE.length],
        backgroundColor: PALETTE[i % PALETTE.length],
        borderWidth: 2, pointRadius: 2, tension: 0, stepped: "before"
      };
    });
    if (section.totalBox.checked && resp.total) {
      datasets.push({
        label: "Total",
        data: resp.total.map(function (p) { return { x: p.t * 1000, y: p.cents / 100 }; }),
        borderColor: totalColor(), backgroundColor: totalColor(),
        borderWidth: 3, borderDash: [6, 4], pointRadius: 0, tension: 0, stepped: "before"
      });
    }
    var c = themeColors();
    if (section.chart) { section.chart.destroy(); }
    section.chart = new Chart(section.canvas, {
      type: "line",
      data: { datasets: datasets },
      options: {
        responsive: true, maintainAspectRatio: false, animation: false,
        interaction: { mode: "nearest", intersect: false },
        parsing: false, normalized: true,
        scales: {
          x: {
            type: "linear",
            ticks: { color: c.text, maxTicksLimit: 6, callback: function (v) { return new Date(v).toLocaleDateString(); } },
            grid: { color: c.grid }
          },
          y: {
            ticks: { color: c.text, callback: function (v) { return iskFmt.format(v); } },
            grid: { color: c.grid }
          }
        },
        plugins: {
          legend: { labels: { color: c.text } },
          tooltip: {
            callbacks: {
              title: function (items) { return items.length ? new Date(items[0].parsed.x).toLocaleString() : ""; },
              label: function (item) { return item.dataset.label + ": " + iskFmt.format(item.parsed.y) + " ISK"; }
            }
          }
        }
      }
    });
  }

  function refreshChart(section) {
    var ids = Array.from(section.selected);
    var note = section.note;
    note.textContent = "";
    if (ids.length === 0) {
      section.token++;
      if (section.chart) { section.chart.destroy(); section.chart = null; }
      note.textContent = "Select at least one wallet.";
      return;
    }
    if (ids.length > MAX_IDS) {
      section.token++;
      note.textContent = "Too many wallets selected (maximum " + MAX_IDS + ").";
      return;
    }
    var q = new URLSearchParams();
    q.set("wallet_ids", ids.join(","));
    if (state.range > 0) {
      q.set("from", new Date(Date.now() - state.range * 1000).toISOString());
    }
    if (section.totalBox.checked) { q.set("total", "1"); }
    var token = ++section.token;
    getJSON("/api/series?" + q.toString()).then(function (resp) {
      if (token !== section.token) { return; }
      drawChart(section, resp);
      var any = resp.series.some(function (s) { return s.points.length > 0; });
      note.textContent = any ? "" : "No data points in this range.";
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
