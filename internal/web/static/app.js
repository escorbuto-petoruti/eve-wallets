// EVE wallets front end. Plain JavaScript, no build step.
// All server text is inserted with textContent, never as HTML.
(function () {
  "use strict";

  var PALETTE = ["#61cce5", "#8ed7bc", "#f0b36b", "#b69cff", "#ff8a8a",
    "#5aa9ff", "#f28dc4", "#c3e07a", "#ff9f6e", "#9aa7b4"];
  var TOTAL_COLOR = "#e9f0f6";
  var MAX_IDS = 50; // keep in sync with the server cap

  var state = { wallets: [], range: 2592000, sections: [], active: null, loyaltyToken: 0, lpTab: null, lpActive: false, lpLoadedAt: 0, lpLoading: false, hasData: false };
  var LP_STALE_MS = 60000; // reopening the loyalty tab refreshes data older than this

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

  // requestJSON rejects with err.unauthorized for a 401; the page has already
  // switched to the signed-out view by then, callers just stay quiet.
  function requestJSON(url, init) {
    var epoch = session.epoch;
    return fetch(url, init).then(function (r) {
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

  function getJSON(url) {
    return requestJSON(url, { headers: { "Accept": "application/json" } });
  }

  // postJSON sends a same-origin JSON POST (the server refuses anything else).
  function postJSON(url, payload) {
    return requestJSON(url, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Accept": "application/json", "Content-Type": "application/json" },
      body: JSON.stringify(payload)
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
    clearLoyalty();
    state.hasData = false;
    $("controls").hidden = true;
    $("tabs").hidden = true;
    $("signed-in").hidden = true;
    $("user-bar").hidden = true;
    $("session-message").textContent = message || "";
    $("signed-out").hidden = false;
  }

  // showStopped replaces the whole page once the server accepted the shutdown:
  // nothing can load any more, so polling stops and every old view is dropped.
  function showStopped() {
    session.signedIn = false;
    session.epoch++;
    stopPolling();
    clearSections();
    clearLoyalty();
    state.hasData = false;
    $("controls").hidden = true;
    $("tabs").hidden = true;
    $("signed-in").hidden = true;
    $("user-bar").hidden = true;
    $("signed-out").hidden = true;
    $("stopped").hidden = false;
  }

  // wireQuit sets up the Quit button: a first click only asks for confirmation
  // inline; the second one calls the shutdown endpoint.
  function wireQuit() {
    var openBtn = $("quit-open");
    var box = $("quit-confirm");
    var yes = $("quit-yes");
    var no = $("quit-no");
    var err = $("quit-error");
    function setConfirm(open) {
      box.hidden = !open;
      openBtn.hidden = open;
      err.textContent = "";
      (open ? no : openBtn).focus();
    }
    openBtn.addEventListener("click", function () { setConfirm(true); });
    no.addEventListener("click", function () { setConfirm(false); });
    box.addEventListener("keydown", function (e) { if (e.key === "Escape") { setConfirm(false); } });
    yes.addEventListener("click", function () {
      yes.disabled = true;
      no.disabled = true;
      postJSON("/api/shutdown", {}).then(function () {
        showStopped();
      }).catch(function (e) {
        yes.disabled = false;
        no.disabled = false;
        if (e.unauthorized) { return; } // the signed-out view is already shown
        setConfirm(false);
        err.textContent = "Could not stop eve-wallets: " + e.message;
      });
    });
  }

  function showSignedIn(me) {
    session.signedIn = true;
    session.epoch++;
    $("signed-out").hidden = true;
    $("user-name").textContent = me.name;
    $("characters").textContent = (me.characters || []).map(function (c) { return c.name; }).join(", ");
    $("user-bar").hidden = false;
    $("signed-in").hidden = false;
    $("tabs").hidden = false;
  }

  var MAX_NAME = 64; // keep in sync with the server limit

  var iskFmt = new Intl.NumberFormat(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
  function formatISK(cents) { return iskFmt.format(cents / 100); }
  function formatTime(unix) { return new Date(unix * 1000).toLocaleString(); }

  // walletLabel is the wallet name. Wallets are always shown inside their
  // owner's tab, so the owner name is not repeated.
  function walletLabel(w) {
    return w.name || (w.kind === "character" ? w.owner_name : w.division === 1 ? "Master Wallet" : "Division " + w.division);
  }
  // canRename mirrors the server rule: corporation wallets other than the
  // Master Wallet whose name does not come from ESI. The server enforces it.
  function canRename(w) {
    return w.kind === "corporation" && w.division !== 1 && w.name_source !== "esi";
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
      s.tab.remove();
    });
    state.sections = [];
    state.active = null;
    $("sections").replaceChildren();
    syncLoyaltyTab();
  }

  // makeTab builds one tab button of the shared tablist (owners and loyalty).
  function makeTab(id, label, panelId, onClick) {
    var tab = el("button", label, "tab");
    tab.type = "button";
    tab.id = id;
    tab.setAttribute("role", "tab");
    tab.setAttribute("aria-controls", panelId);
    tab.setAttribute("aria-selected", "false");
    tab.tabIndex = -1;
    tab.addEventListener("click", onClick);
    return tab;
  }

  // The loyalty tab is the last tab of the bar; owner tabs go before it.
  function syncLoyaltyTab() {
    state.lpTab.setAttribute("aria-selected", state.lpActive ? "true" : "false");
    // Keep one tab in the tab order even when there are no owner tabs.
    state.lpTab.tabIndex = state.lpActive || !state.sections.length ? 0 : -1;
    $("loyalty-panel").hidden = !state.lpActive;
    // Time ranges do not apply to loyalty points.
    $("controls").hidden = state.lpActive || !state.hasData;
  }

  function buildSection(group, idx) {
    var section = { group: group, charts: [], token: 0, note: null, grid: null, tab: null, panel: null, drawn: false, rowNames: new Map(), movements: null, movementsBox: null };
    var card = el("section", undefined, "card");
    card.id = "panel-" + idx;
    card.setAttribute("role", "tabpanel");
    card.setAttribute("aria-labelledby", "tab-" + idx);
    card.hidden = true;
    section.panel = card;
    var tab = makeTab("tab-" + idx, group.title, card.id, function () { activateSection(section); });
    section.tab = tab;
    var tabLogo = eveImage("corporation", group.corpId, 24);
    if (tabLogo) { tab.insertBefore(tabLogo, tab.firstChild); }
    $("tabs").insertBefore(tab, state.lpTab);
    var title = el("h2", group.title);
    var titleLogo = eveImage("corporation", group.corpId, 32);
    if (titleLogo) { title.insertBefore(titleLogo, title.firstChild); }
    card.appendChild(title);

    section.note = el("p", "", "muted");
    section.note.setAttribute("role", "status");
    card.appendChild(section.note);
    section.grid = el("div", undefined, "panels");
    card.appendChild(section.grid);
    section.movements = buildMovements(section);
    card.appendChild(section.movementsBox);

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
      var rowName = document.createTextNode(" " + walletLabel(w));
      section.rowNames.set(w.id, rowName);
      nameCell.appendChild(rowName);
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
    state.lpActive = false;
    syncLoyaltyTab();
    refreshChart(section);
  }

  // activateLoyalty shows the loyalty panel and loads it the first time, or
  // again when it never loaded or is older than LP_STALE_MS.
  function activateLoyalty() {
    state.sections.forEach(function (s) {
      s.token++;
      destroyCharts(s);
      s.drawn = false;
      s.panel.hidden = true;
      s.tab.setAttribute("aria-selected", "false");
      s.tab.tabIndex = -1;
    });
    state.active = null;
    state.lpActive = true;
    syncLoyaltyTab();
    if (!state.lpLoading && (!state.lpLoadedAt || Date.now() - state.lpLoadedAt > LP_STALE_MS)) { loadLoyalty(); }
  }

  // Every tab of the bar in order: owners, then loyalty points.
  function allTabs() {
    var list = state.sections.map(function (s) {
      return { tab: s.tab, go: function () { activateSection(s); } };
    });
    list.push({ tab: state.lpTab, go: activateLoyalty });
    return list;
  }

  function onTabKey(ev) {
    var tabs = allTabs();
    var n = tabs.length;
    var i = tabs.findIndex(function (t) { return t.tab === ev.target; });
    if (i < 0 || n === 0) { return; }
    var next;
    if (ev.key === "ArrowRight") { next = (i + 1) % n; }
    else if (ev.key === "ArrowLeft") { next = (i + n - 1) % n; }
    else if (ev.key === "Home") { next = 0; }
    else if (ev.key === "End") { next = n - 1; }
    else { return; }
    ev.preventDefault();
    tabs[next].go();
    tabs[next].tab.focus();
  }

  // The selected tab survives a reload while its owner still exists; an active
  // loyalty tab stays active.
  function renderSections() {
    var prev = state.active ? state.active.group.key : null;
    clearSections();
    groupWallets(state.wallets).forEach(function (g, i) {
      state.sections.push(buildSection(g, i));
    });
    if (state.lpActive) { activateLoyalty(); return; }
    if (!state.sections.length) { return; }
    var pick = state.sections.find(function (s) { return s.group.key === prev; }) || state.sections[0];
    activateSection(pick);
  }

  var lpFmt = new Intl.NumberFormat(undefined, { maximumFractionDigits: 0 });

  // clearLoyalty empties the loyalty view and bumps its token so a late
  // response is dropped.
  function clearLoyalty() {
    state.loyaltyToken++;
    state.lpActive = false;
    state.lpLoadedAt = 0;
    state.lpLoading = false;
    $("loyalty").replaceChildren();
    $("loyalty-status").textContent = "";
    $("loyalty-status").className = "muted";
    syncLoyaltyTab();
  }

  function setLoyaltyStatus(text, cls) {
    var box = $("loyalty-status");
    box.textContent = text;
    box.className = cls;
  }

  // renderLoyalty shows, per character, a table of corporations (sorted as the
  // API returns them) or, when its token lacks the scope, a sign-in notice.
  function renderLoyalty(resp) {
    var box = $("loyalty");
    box.replaceChildren();
    var chars = resp.characters || [];
    if (!chars.length) {
      setLoyaltyStatus("No characters registered.", "muted");
      return;
    }
    var latest = 0;
    chars.forEach(function (c) {
      var wrap = el("div", undefined, "loyalty-character");
      var head = el("h3", c.character_name);
      var portrait = eveImage("character", c.character_id, 24);
      if (portrait) { head.insertBefore(portrait, head.firstChild); }
      wrap.appendChild(head);
      var corps = c.corporations || [];
      if (c.needs_reauth) {
        var note = el("div", undefined, "warn reauth-notice");
        note.setAttribute("role", "alert");
        note.appendChild(el("p", c.character_name + ": sign in again to show loyalty points", "warn"));
        var link = el("a", "Sign in again", "button");
        link.href = "/auth/add-character";
        note.appendChild(link);
        note.appendChild(el("p", "On the EVE login screen, choose " + c.character_name + ".", "muted"));
        wrap.appendChild(note);
      } else if (!corps.length) {
        wrap.appendChild(el("p", "No loyalty points.", "muted"));
      } else {
        if (c.fetched_at && c.fetched_at > latest) { latest = c.fetched_at; }
        var scroll = el("div", undefined, "scroll");
        var table = el("table", undefined, "loyalty-table");
        var thead = document.createElement("thead");
        var hr = document.createElement("tr");
        hr.appendChild(el("th", "Corporation"));
        hr.appendChild(el("th", "Points", "num"));
        thead.appendChild(hr);
        table.appendChild(thead);
        var tbody = document.createElement("tbody");
        corps.forEach(function (k) {
          var tr = document.createElement("tr");
          var name = el("td");
          var cell = el("div", undefined, "loyalty-corp");
          var logo = eveImage("corporation", k.corporation_id, 24);
          if (logo) { cell.appendChild(logo); }
          cell.appendChild(el("span", k.name));
          name.appendChild(cell);
          tr.appendChild(name);
          tr.appendChild(el("td", lpFmt.format(k.points), "num"));
          tbody.appendChild(tr);
        });
        table.appendChild(tbody);
        scroll.appendChild(table);
        wrap.appendChild(scroll);
      }
      box.appendChild(wrap);
    });
    setLoyaltyStatus(latest ? "Last updated " + formatTime(latest) + "." : "", "muted");
  }

  function loadLoyalty() {
    var token = ++state.loyaltyToken;
    state.lpLoading = true;
    setLoyaltyStatus("Loading loyalty points…", "muted");
    getJSON("/api/loyalty").then(function (resp) {
      if (token === state.loyaltyToken) {
        state.lpLoading = false;
        state.lpLoadedAt = Date.now();
        renderLoyalty(resp);
      }
    }).catch(function (err) {
      if (!err.unauthorized && token === state.loyaltyToken) {
        state.lpLoading = false;
        $("loyalty").replaceChildren();
        setLoyaltyStatus("Cannot load loyalty points: " + err.message, "bad");
      }
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

  // buildRenameControl returns the inline rename form of a wallet panel: a
  // Rename button that opens a text field with Save, Cancel and (for a custom
  // name) Reset name. Server text only goes through textContent or .value.
  // onChange runs after a successful save so the panel can show the new name.
  function buildRenameControl(section, w, focusTarget, onChange) {
    var box = el("div", undefined, "rename");
    var openBtn = el("button", "Rename", "rename-open");
    openBtn.type = "button";
    var form = el("form", undefined, "rename-form");
    form.hidden = true;
    var inputId = "rename-input-" + w.id;
    var label = el("label", "Wallet name", "rename-label");
    label.setAttribute("for", inputId);
    var input = document.createElement("input");
    input.type = "text";
    input.id = inputId;
    input.maxLength = MAX_NAME;
    input.autocomplete = "off";
    var save = el("button", "Save", "primary");
    save.type = "submit";
    var cancel = el("button", "Cancel");
    cancel.type = "button";
    var reset = el("button", "Reset name");
    reset.type = "button";
    var status = el("p", "", "muted rename-status");
    status.setAttribute("role", "status");
    status.setAttribute("aria-live", "polite");
    var actions = el("div", undefined, "rename-actions");
    [save, cancel, reset].forEach(function (b) { actions.appendChild(b); });
    [label, input, actions].forEach(function (n) { form.appendChild(n); });
    box.appendChild(openBtn);
    box.appendChild(form);
    box.appendChild(status);

    function sync() {
      openBtn.hidden = !canRename(w) || !form.hidden;
      reset.hidden = w.name_source !== "custom";
    }
    function setBusy(on) {
      [input, save, cancel, reset].forEach(function (n) { n.disabled = on; });
    }
    function close() {
      form.hidden = true;
      sync();
      if (!openBtn.hidden) { openBtn.focus(); } else if (focusTarget) { focusTarget.focus(); }
    }
    function open() {
      status.textContent = "";
      input.value = w.name_source === "custom" ? w.name : "";
      input.placeholder = w.name_source === "custom" ? "" : w.name;
      form.hidden = false;
      sync();
      input.focus();
      input.select();
    }
    function send(name) {
      status.textContent = "Saving…";
      setBusy(true);
      postJSON("/api/wallets/" + encodeURIComponent(String(w.id)) + "/label", { name: name }).then(function (resp) {
        w.name = resp.name;
        w.name_source = resp.name_source;
        var rowName = section.rowNames.get(w.id);
        if (rowName) { rowName.nodeValue = " " + walletLabel(w); }
        setBusy(false);
        status.textContent = name === "" ? "Name reset." : "Name saved.";
        onChange();
        close();
      }).catch(function (err) {
        if (err.unauthorized) { return; }
        setBusy(false);
        status.textContent = err.message;
        input.focus();
      });
    }
    openBtn.addEventListener("click", open);
    cancel.addEventListener("click", function () { status.textContent = ""; close(); });
    reset.addEventListener("click", function () { send(""); });
    form.addEventListener("submit", function (ev) {
      ev.preventDefault();
      send(input.value);
    });
    input.addEventListener("keydown", function (ev) {
      if (ev.key === "Escape") { ev.preventDefault(); status.textContent = ""; close(); }
    });
    sync();
    return box;
  }

  var JOURNAL_PAGE = 50; // rows per request, within the server cap

  function signedISK(cents) { return cents > 0 ? "+" + formatISK(cents) : formatISK(cents); }

  // dayBound turns a date input value (YYYY-MM-DD, local time) into an RFC 3339
  // instant at the start or the end of that day, or "" when it is empty.
  function dayBound(value, end) {
    if (!value) { return ""; }
    var d = new Date(value + (end ? "T23:59:59" : "T00:00:00"));
    return isNaN(d.getTime()) ? "" : d.toISOString();
  }

  // buildMovements returns the controller of a section's movements view: a
  // table of the stored journal of one wallet with type and date filters and
  // Previous/Next paging over the keyset cursor, shown in a modal dialog that
  // fits the viewport. Server text only goes through textContent.
  function buildMovements(section) {
    var box = document.createElement("dialog");
    box.className = "movements movements-dialog";
    var modal = typeof box.showModal === "function";
    box.hidden = true;
    var title = el("h3", "", "movements-title");
    title.id = "movements-title-" + section.panel.id;
    box.setAttribute("aria-labelledby", title.id);
    title.tabIndex = -1;
    var closeBtn = el("button", "Close", "movements-close");
    closeBtn.type = "button";
    var head = el("div", undefined, "row movements-head");
    head.appendChild(title);
    head.appendChild(closeBtn);
    box.appendChild(head);

    var form = el("form", undefined, "movements-filters");
    var id = "movements-" + section.panel.id;
    function field(text, input, key) {
      var wrap = el("div", undefined, "movements-field");
      var label = el("label", text, "rename-label");
      input.id = id + "-" + key;
      label.setAttribute("for", input.id);
      wrap.appendChild(label);
      wrap.appendChild(input);
      form.appendChild(wrap);
    }
    var typeSel = document.createElement("select");
    var from = document.createElement("input");
    from.type = "date";
    var to = document.createElement("input");
    to.type = "date";
    field("Type", typeSel, "type");
    field("From", from, "from");
    field("To", to, "to");
    var apply = el("button", "Apply", "primary");
    apply.type = "submit";
    form.appendChild(apply);
    box.appendChild(form);

    var status = el("p", "", "muted movements-status");
    status.setAttribute("role", "status");
    status.setAttribute("aria-live", "polite");
    box.appendChild(status);

    // The table scrolls in the middle region of the dialog, between the fixed
    // top area and the pager; it is focusable so keyboard users can scroll it.
    var scroll = el("div", undefined, "scroll movements-scroll movements-body");
    scroll.setAttribute("tabindex", "0");
    scroll.setAttribute("aria-label", "Movements table");
    var table = document.createElement("table");
    table.className = "movements-table";
    var tr = document.createElement("tr");
    [["Date"], ["Type"], ["Amount (ISK)", "num"], ["Description"]].forEach(function (c) { tr.appendChild(el("th", c[0], c[1])); });
    var thead = document.createElement("thead");
    thead.appendChild(tr);
    table.appendChild(thead);
    var tbody = document.createElement("tbody");
    table.appendChild(tbody);
    scroll.appendChild(table);
    box.appendChild(scroll);
    var pager = el("div", undefined, "movements-pager");
    var prev = el("button", "Previous", "movements-prev");
    prev.type = "button";
    var next = el("button", "Next", "movements-next");
    next.type = "button";
    var pageNo = el("span", "", "muted movements-page");
    pageNo.setAttribute("aria-live", "polite");
    pager.appendChild(prev);
    pager.appendChild(pageNo);
    pager.appendChild(next);
    box.appendChild(pager);

    // cur.stack holds the cursor used for each visited page (page 1 has none),
    // so its length is the current page number; cur.next is the cursor of the
    // page after the one shown.
    var cur = { wallet: null, trigger: null, stack: [], next: null, busy: false, seq: 0, typesLoaded: false };

    function syncPager() {
      prev.disabled = cur.busy || cur.stack.length <= 1;
      next.disabled = cur.busy || !cur.next;
    }

    function url(cursor) {
      var q = ["limit=" + JOURNAL_PAGE];
      if (typeSel.value) { q.push("ref_type=" + encodeURIComponent(typeSel.value)); }
      var f = dayBound(from.value, false);
      var t = dayBound(to.value, true);
      if (f) { q.push("from=" + encodeURIComponent(f)); }
      if (t) { q.push("to=" + encodeURIComponent(t)); }
      if (cursor) { q.push("cursor=" + encodeURIComponent(cursor)); }
      return "/api/wallets/" + encodeURIComponent(String(cur.wallet.id)) + "/journal?" + q.join("&");
    }

    function showRows(entries) {
      tbody.replaceChildren();
      entries.forEach(function (e) {
        var row = document.createElement("tr");
        row.appendChild(el("td", formatTime(e.date), "movements-date"));
        var typeCell = el("td", e.ref_type, "movements-type");
        typeCell.setAttribute("title", e.ref_type);
        row.appendChild(typeCell);
        row.appendChild(el("td", signedISK(e.cents), e.cents < 0 ? "num amount loss" : "num amount gain"));
        // Truncated with an ellipsis in CSS; the title keeps the full text.
        var descCell = el("td", e.description, "movements-desc");
        descCell.setAttribute("title", e.description);
        row.appendChild(descCell);
        tbody.appendChild(row);
      });
      scroll.scrollTop = 0;
    }

    function fillTypes(types) {
      var keep = typeSel.value;
      typeSel.replaceChildren();
      var all = el("option", "All types");
      all.value = "";
      typeSel.appendChild(all);
      types.forEach(function (t) {
        var o = el("option", t);
        o.value = t;
        typeSel.appendChild(o);
      });
      typeSel.value = types.indexOf(keep) >= 0 ? keep : "";
    }

    // load fetches the page reached through stack (its last item is the
    // cursor, empty for page 1) and replaces the table with it. The stack is
    // only committed once the page arrived, so a failed step keeps the view.
    function load(stack, pressed) {
      if (!cur.wallet) { return; }
      var f = dayBound(from.value, false);
      var t = dayBound(to.value, true);
      if (f && t && t < f) { status.textContent = "The end date is before the start date."; return; }
      var seq = ++cur.seq;
      status.textContent = "Loading…";
      cur.busy = true;
      syncPager();
      getJSON(url(stack[stack.length - 1])).then(function (resp) {
        if (seq !== cur.seq) { return; }
        if (!cur.typesLoaded) { fillTypes(resp.ref_types || []); cur.typesLoaded = true; }
        showRows(resp.entries || []);
        cur.stack = stack;
        cur.next = resp.next_cursor || null;
        cur.busy = false;
        pageNo.textContent = "Page " + stack.length;
        syncPager();
        var n = tbody.rows.length;
        status.textContent = n === 0 ? "No movements found." : n + (n === 1 ? " movement shown." : " movements shown.");
        if (pressed) { (pressed.disabled ? title : pressed).focus(); }
      }).catch(function (err) {
        if (err.unauthorized || seq !== cur.seq) { return; }
        cur.busy = false;
        syncPager();
        status.textContent = "Could not load movements: " + err.message;
        if (pressed && !pressed.disabled) { pressed.focus(); }
      });
    }

    // reset drops the loaded state; it runs on every way of closing.
    function reset() {
      cur.seq++;
      cur.wallet = null;
      cur.busy = false;
      box.hidden = true;
      if (cur.trigger) { cur.trigger.focus(); }
    }
    function close() {
      if (modal && box.open) { box.close(); } else { reset(); }
    }
    function openMovements(w, trigger) {
      cur.wallet = w;
      cur.trigger = trigger;
      cur.typesLoaded = false;
      cur.stack = [];
      cur.next = null;
      cur.busy = false;
      pageNo.textContent = "";
      syncPager();
      tbody.replaceChildren();
      typeSel.replaceChildren();
      from.value = "";
      to.value = "";
      title.textContent = "Movements: " + walletLabel(w);
      box.hidden = false;
      if (modal) { if (!box.open) { box.showModal(); } }
      closeBtn.focus();
      load([""]);
    }
    closeBtn.addEventListener("click", close);
    next.addEventListener("click", function () {
      if (cur.busy || !cur.next) { return; }
      load(cur.stack.concat([cur.next]), next);
    });
    prev.addEventListener("click", function () {
      if (cur.busy || cur.stack.length <= 1) { return; }
      load(cur.stack.slice(0, -1), prev);
    });
    form.addEventListener("submit", function (ev) { ev.preventDefault(); load([""]); });
    // Escape fires "cancel" and then "close" on a modal dialog; "close" also
    // follows box.close(), so it is the single place that resets the state.
    box.addEventListener("close", reset);
    box.addEventListener("cancel", function () { cur.seq++; });
    box.addEventListener("keydown", function (ev) {
      if (ev.key === "Escape" && !modal) { ev.preventDefault(); close(); }
    });
    section.movementsBox = box;
    return { open: openMovements, close: close };
  }

  // buildPanel adds one small multiple: title, current balance, delta and a
  // canvas with its own Y scale.
  function buildPanel(section, opts) {
    var panel = el("article", undefined, "panel " + opts.cls);
    var h = el("h3", undefined, "panel-title");
    h.tabIndex = -1; // focus target when the rename control has no button to return to
    if (opts.image) { h.appendChild(opts.image); }
    var titleText = document.createTextNode(opts.label);
    h.appendChild(titleText);
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
    if (opts.wallet) {
      var w = opts.wallet;
      var describe = function () {
        canvas.setAttribute("aria-label", "Balance history for " + walletLabel(w) + ": " + (hasBalance ? formatISK(last) + " ISK now. " : "") + d.text);
      };
      panel.insertBefore(buildRenameControl(section, w, h, function () {
        titleText.nodeValue = walletLabel(w);
        describe();
      }), h.nextSibling);
      var moves = el("button", "Movements", "movements-open");
      moves.type = "button";
      moves.addEventListener("click", function () { section.movements.open(w, moves); });
      panel.insertBefore(moves, box);
    }
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
        points: byId.get(w.id) || [], cents: w.cents, wallet: w
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
    state.lpTab = makeTab("tab-loyalty", "Loyalty points", "loyalty-panel", activateLoyalty);
    $("tabs").appendChild(state.lpTab);
    syncLoyaltyTab();
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
        state.hasData = true;
        // The first collection finished while polling: the stored points are new.
        if (tries > 0) { state.lpLoadedAt = 0; }
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

  wireQuit();
  init();
})();
