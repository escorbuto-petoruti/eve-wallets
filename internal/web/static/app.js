// EVE wallets front end. Plain JavaScript, no build step.
// All server text is inserted with textContent, never as HTML.
(function () {
  "use strict";

  var PALETTE = ["#2563eb", "#dc2626", "#16a34a", "#d97706", "#7c3aed",
    "#0891b2", "#db2777", "#65a30d", "#ea580c", "#4b5563"];
  var TOTAL_COLOR = "#e5e5e5";
  var MAX_IDS = 50; // keep in sync with the server cap

  var state = { wallets: [], selected: new Set(), range: 2592000, chart: null, token: 0 };

  var $ = function (id) { return document.getElementById(id); };

  function el(tag, text, cls) {
    var n = document.createElement(tag);
    if (text !== undefined) { n.textContent = text; }
    if (cls) { n.className = cls; }
    return n;
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
    state.token++;
    stopPolling();
    if (state.chart) { state.chart.destroy(); state.chart = null; }
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
    $("user-bar").hidden = false;
    $("signed-in").hidden = false;
  }

  var iskFmt = new Intl.NumberFormat(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
  function formatISK(cents) { return iskFmt.format(cents / 100); }
  function formatTime(unix) { return new Date(unix * 1000).toLocaleString(); }

  // walletLabel is the character name or "<corporation> \u00b7 <name>".
  function walletLabel(w) {
    var name = w.name || (w.kind === "character" ? w.owner_name : w.division === 1 ? "Master Wallet" : "Division " + w.division);
    return w.kind === "character" ? name : w.owner_name + " \u00b7 " + name;
  }
  function ownerLabel(w) {
    return w.owner_name + " (" + (w.kind === "character" ? "character" : "corporation") + ")";
  }

  function renderPicker() {
    var picker = $("picker");
    picker.replaceChildren();
    var groups = new Map();
    state.wallets.forEach(function (w) {
      var key = w.kind + ":" + w.owner_id;
      if (!groups.has(key)) { groups.set(key, []); }
      groups.get(key).push(w);
    });
    groups.forEach(function (list) {
      var fs = el("fieldset");
      fs.appendChild(el("legend", ownerLabel(list[0])));
      var box = el("div", undefined, "wallets");
      list.forEach(function (w) {
        var label = el("label", undefined, "check");
        var cb = document.createElement("input");
        cb.type = "checkbox";
        cb.checked = state.selected.has(w.id);
        cb.addEventListener("change", function () {
          if (cb.checked) { state.selected.add(w.id); } else { state.selected.delete(w.id); }
          refreshChart();
        });
        label.appendChild(cb);
        label.appendChild(document.createTextNode(" " + walletLabel(w)));
        box.appendChild(label);
      });
      fs.appendChild(box);
      picker.appendChild(fs);
    });
  }

  function renderLatest() {
    var body = $("latest").tBodies[0];
    body.replaceChildren();
    state.wallets.forEach(function (w) {
      if (w.cents === null || w.cents === undefined) { return; }
      var tr = document.createElement("tr");
      tr.appendChild(el("td", ownerLabel(w)));
      tr.appendChild(el("td", walletLabel(w)));
      tr.appendChild(el("td", formatISK(w.cents), "num"));
      tr.appendChild(el("td", formatTime(w.balance_time)));
      body.appendChild(tr);
    });
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
    list("Skipped:", s.skipped.map(function (k) { return k.owner + ": " + k.reason; }), "warn");
    list("Errors:", s.errors, "bad");
  }

  function themeColors() {
    var cs = getComputedStyle(document.body);
    return { text: cs.color, grid: cs.getPropertyValue("--border").trim() };
  }

  function totalColor() {
    return getComputedStyle(document.body).color || TOTAL_COLOR;
  }

  function drawChart(resp) {
    var datasets = resp.series.map(function (s, i) {
      var w = state.wallets.find(function (x) { return x.id === s.wallet_id; });
      return {
        label: w ? walletLabel(w) : "Wallet " + s.wallet_id,
        data: s.points.map(function (p) { return { x: p.t * 1000, y: p.cents / 100 }; }),
        borderColor: PALETTE[i % PALETTE.length],
        backgroundColor: PALETTE[i % PALETTE.length],
        borderWidth: 2, pointRadius: 2, tension: 0, stepped: "before"
      };
    });
    if ($("total").checked && resp.total) {
      datasets.push({
        label: "Total",
        data: resp.total.map(function (p) { return { x: p.t * 1000, y: p.cents / 100 }; }),
        borderColor: totalColor(), backgroundColor: totalColor(),
        borderWidth: 3, borderDash: [6, 4], pointRadius: 0, tension: 0, stepped: "before"
      });
    }
    var c = themeColors();
    if (state.chart) { state.chart.destroy(); }
    state.chart = new Chart($("chart"), {
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

  function refreshChart() {
    var ids = Array.from(state.selected);
    var note = $("chart-note");
    note.textContent = "";
    if (ids.length === 0) {
      if (state.chart) { state.chart.destroy(); state.chart = null; }
      note.textContent = "Select at least one wallet.";
      return;
    }
    if (ids.length > MAX_IDS) {
      note.textContent = "Too many wallets selected (maximum " + MAX_IDS + ").";
      return;
    }
    var q = new URLSearchParams();
    q.set("wallet_ids", ids.join(","));
    if (state.range > 0) {
      q.set("from", new Date(Date.now() - state.range * 1000).toISOString());
    }
    if ($("total").checked) { q.set("total", "1"); }
    var token = ++state.token;
    getJSON("/api/series?" + q.toString()).then(function (resp) {
      if (token !== state.token) { return; }
      drawChart(resp);
      var any = resp.series.some(function (s) { return s.points.length > 0; });
      note.textContent = any ? "" : "No data points in this range.";
    }).catch(function (err) {
      if (!err.unauthorized && token === state.token) { note.textContent = err.message; }
    });
  }

  function setRange(btn) {
    state.range = Number(btn.getAttribute("data-range"));
    document.querySelectorAll("#ranges button").forEach(function (b) {
      var on = b === btn;
      b.classList.toggle("active", on);
      b.setAttribute("aria-pressed", on ? "true" : "false");
    });
    refreshChart();
  }

  function init() {
    document.querySelectorAll("#ranges button").forEach(function (b) {
      b.addEventListener("click", function () { setRange(b); });
    });
    $("total").addEventListener("change", refreshChart);

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
        state.selected = new Set();
        state.wallets.slice(0, MAX_IDS).forEach(function (w) { state.selected.add(w.id); });
        ["controls", "chart-card", "latest-card"].forEach(function (id) { $(id).hidden = false; });
        renderPicker();
        renderLatest();
        refreshChart();
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
