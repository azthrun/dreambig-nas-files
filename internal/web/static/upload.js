/* Upload panel on a folder page: progressive enhancement over tus-js-client (vendored,
   no CDN). Without JavaScript the panel stays hidden and browsing works as before.
   Uses no secure-context API (the site is plain HTTP, ADR-0002): no Web Crypto,
   service workers or File System Access API. Folders come from the <input
   webkitdirectory> picker or from dropped DataTransferItem.webkitGetAsEntry() entries.
   A Conflict is settled in one dialog with an apply-to-all choice (the Uploads in flight
   queue for it). Every file is its own tus Upload at dest + "/" + its relative path; the server creates
   missing folders when the file completes, so an empty folder is never created.
   Texts come from data-* attributes the server renders in the User's language.
   Browser behaviour is checked by hand. */
(function () {
  "use strict";
  var panel = document.getElementById("upload");
  if (!panel || !window.tus || !window.File) { return; }
  panel.hidden = false;

  var MAX_PARALLEL = 3;     // Uploads in flight at once
  var KEEP_ROWS = 200;      // above this many files, finished rows are dropped from the page

  var meta = document.querySelector('meta[name="csrf-token"]');
  var token = meta ? meta.getAttribute("content") : "";
  var endpoint = panel.getAttribute("data-endpoint");
  var dest = panel.getAttribute("data-dest");
  var input = document.getElementById("upload-files");
  var folderInput = document.getElementById("upload-folder");
  var folderLabel = document.getElementById("upload-folder-label");
  var startBtn = document.getElementById("upload-start");
  var cancelBtn = document.getElementById("upload-cancel");
  var list = document.getElementById("upload-list");
  var total = document.getElementById("upload-total");
  var totalText = document.getElementById("upload-total-text");
  var status = document.getElementById("upload-status");
  var activity = document.getElementById("upload-activity");
  var count = document.getElementById("upload-count");
  var ask = document.getElementById("upload-conflict");
  var askName = document.getElementById("upload-conflict-name");
  var askText = document.getElementById("upload-conflict-text");
  var askAll = document.getElementById("upload-conflict-all");

  if (folderInput && !("webkitdirectory" in folderInput) && folderLabel) { folderLabel.hidden = true; }

  function msg(key) { return panel.getAttribute("data-msg-" + key) || ""; }
  function fmt(text, args) {
    var i = 0;
    return text.replace(/%d/g, function () { return String(args[i++]); });
  }

  var items = [];   // {file, rel, row, bar, text, cancel, upload, sent, state, settled}
  var cursor = 0;   // items before it are no longer waiting
  var active = 0;   // Uploads in flight
  var reading = 0;  // dropped folders still being read
  var skippedEmpty = 0;
  // The User's "apply to all" Conflict choice for the rest of this selection: "", "skip",
  // "keep" or "replace".
  var allChoice = "";
  var askQueue = [];   // items waiting for the one Conflict dialog
  var asking = null;   // the item the dialog is about
  var chosen = [];     // picked files not started yet: {file, rel}

  function percent(a, b) { return b > 0 ? Math.floor((a * 100) / b) : 100; }

  // Names are sent in NFC (macOS file systems hand out NFD); the server normalises too.
  function nfc(s) { return s.normalize ? s.normalize("NFC") : s; }

  // Mirrors mounts.IsHiddenName: DSM and service folders are never uploaded.
  function isHidden(name) {
    var n = name.toLowerCase();
    return name.charAt(0) === "@" || name.charAt(0) === "#" || n === ".ds_store" ||
      n === ".nas-files-staging" || n === ".nas-files-trash" || n === ".nas-mcp-trash";
  }

  // .DS_Store is the Mac's own bookkeeping, in every folder a Mac has shown: the User did
  // not pick it, so dropping it is not a failure to report.
  function isOSNoise(rel) {
    return rel.split("/").pop().toLowerCase() === ".ds_store";
  }

  function hiddenPath(rel) {
    var segs = rel.split("/");
    for (var i = 0; i < segs.length; i++) { if (isHidden(segs[i])) { return true; } }
    return false;
  }

  // Mirrors i18n.FormatSize.
  function humanSize(n) {
    if (n < 1024) { return n + " " + msg("bytes"); }
    var units = ["KB", "MB", "GB", "TB"], f = n, i = -1;
    do { f /= 1024; i++; } while (f >= 1024 && i < units.length - 1);
    return f.toFixed(1) + " " + units[i];
  }

  function setIcon(row, name) {
    row.querySelector("use").setAttribute("href", "/static/icons.svg#" + name);
  }

  function newRow(rel, onCancel) {
    var row = document.createElement("li");
    row.className = "upload-row";
    var icon = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    icon.setAttribute("class", "icon");
    icon.setAttribute("aria-hidden", "true");
    icon.appendChild(document.createElementNS("http://www.w3.org/2000/svg", "use"));
    var main = document.createElement("div");
    main.className = "upload-main";
    var name = document.createElement("span");
    name.className = "upload-name";
    name.title = rel;
    name.textContent = rel;
    var line = document.createElement("span");
    line.className = "upload-line";
    var text = document.createElement("span");
    text.className = "upload-state";
    var detail = document.createElement("span");
    var bar = document.createElement("progress");
    bar.max = 100;
    bar.value = 0;
    bar.hidden = true;
    var cancel = document.createElement("button");
    cancel.type = "button";
    cancel.className = "link-btn";
    cancel.textContent = msg("cancel");
    cancel.addEventListener("click", onCancel);
    line.appendChild(text);
    line.appendChild(detail);
    main.appendChild(name);
    main.appendChild(line);
    main.appendChild(bar);
    row.appendChild(icon);
    row.appendChild(main);
    row.appendChild(cancel);
    list.appendChild(row);
    return { row: row, bar: bar, text: text, detail: detail, cancel: cancel };
  }

  // Rows are made lazily: a big folder has thousands of waiting files, so above KEEP_ROWS
  // only the ones that run or fail get a line on the page.
  function ensureRow(it) {
    if (it.row) { return; }
    var r = newRow(it.rel, function () { cancelItem(it); });
    it.row = r.row; it.bar = r.bar; it.text = r.text; it.detail = r.detail; it.cancel = r.cancel;
  }

  // The icon says why a row stopped: colour alone must not carry it.
  function iconFor(state, text) {
    switch (state) {
    case "waiting": return "clock";
    case "running": return "loader-circle";
    case "asking": return "circle-help";
    case "done": return "circle-check";
    case "skipped": return "skip-forward";
    case "cancelled": return "circle-minus";
    }
    if (text === msg("refused")) { return "ban"; }
    if (text === msg("bad_name")) { return "file-x"; }
    if (text === msg("no_space")) { return "hard-drive"; }
    return "circle-alert";
  }

  function setDetail(it) {
    if (!it.row) { return; }
    var size = it.file ? it.file.size : 0;
    if (it.state === "running" && it.file) {
      it.detail.textContent = " · " + percent(it.sent, size) + "% · " + humanSize(it.sent) + " / " + humanSize(size);
    } else {
      it.detail.textContent = it.state === "waiting" && it.file ? " · " + humanSize(size) : "";
    }
  }

  function setState(it, state, text) {
    it.state = state;
    if (state !== "waiting" || items.length <= KEEP_ROWS) { ensureRow(it); }
    if (it.row) {
      it.row.setAttribute("data-state", state);
      setIcon(it.row, iconFor(state, text));
      it.text.textContent = text;
      it.bar.hidden = state !== "running";
      setDetail(it);
      it.cancel.hidden = state === "done" || state === "failed" || state === "cancelled" || state === "exists" || state === "skipped";
      if (state === "done" && items.length > KEEP_ROWS) {
        list.removeChild(it.row);
        it.row = null;
      }
    }
  }

  function refreshTotal() {
    var sent = 0, size = 0, over = 0;
    for (var i = 0; i < items.length; i++) {
      size += items[i].file ? items[i].file.size : 0;
      sent += items[i].state === "done" && items[i].file ? items[i].file.size : items[i].sent;
      if (items[i].settled || items[i].state === "cancelled") { over++; }
    }
    var p = percent(sent, size);
    total.value = p;
    totalText.textContent = p + "%";
    count.textContent = fmt(msg("files_of"), [over, items.length]);
  }

  // What was picked, listed before Upload is pressed; a row can still be dropped.
  function showChosen() {
    list.innerHTML = "";
    chosen.slice(0, KEEP_ROWS).forEach(function (c) {
      var r = newRow(c.rel, function () {
        chosen.splice(chosen.indexOf(c), 1);
        showChosen();
      });
      r.row.setAttribute("data-state", "waiting");
      setIcon(r.row, "clock");
      r.text.textContent = msg("waiting");
      r.detail.textContent = " · " + humanSize(c.file.size);
    });
    status.textContent = "";
    total.value = 0;
    totalText.textContent = "0%";
    count.textContent = fmt(msg("files_of"), [0, chosen.length]);
    cancelBtn.hidden = false;
    activity.hidden = chosen.length === 0;
  }

  function clearPicks() {
    input.value = "";
    if (folderInput) { folderInput.value = ""; }
  }

  function failureText(err) {
    var res = err && err.originalResponse;
    var code = res ? res.getStatus() : 0;
    if (code === 507) { return ["failed", msg("no_space")]; } // ERR_INSUFFICIENT_SPACE: free-space reserve
    if (code === 409) { return offsetMismatch(err) ? ["failed", msg("failed")] : ["exists", msg("exists")]; }
    if (code === 403 || code === 404) { return ["failed", msg("refused")]; }
    if (code === 400) { return ["failed", msg("bad_name")]; }
    return ["failed", msg("failed")];
  }

  // Conflicts. A Path that already holds a file is met in two places: at creation (POST,
  // 409) and, if the file appeared while the data was in flight, when the last byte
  // arrives (PATCH, 409; the server keeps the data until we call resolve). A folder at
  // the Path (ERR_CONFLICT_FOLDER) allows no choice but skip, and a file where a folder is
  // needed (ERR_CONFLICT_PARENT) allows none at all: the file fails, the rest go on.
  function conflictBody(err) {
    var res = err && err.originalResponse;
    return res && res.getBody ? String(res.getBody()) : "";
  }

  // tusd answers 409 to a PATCH at the wrong offset too: another tab is sending the same
  // file through the same resume URL. That is no Conflict and nothing waits to be resolved.
  function offsetMismatch(err) { return conflictBody(err).indexOf("ERR_MISMATCHED_OFFSET") >= 0; }

  function isLate(err) {
    var req = err && err.originalRequest;
    return !!req && req.getMethod() === "PATCH";
  }

  function skipped(it) { settle(it, "skipped", msg("skipped")); }

  // reportSkip tells the server a file is not being sent, because no Upload exists yet to
  // carry the choice. The file is skipped whether or not the report arrives.
  function reportSkip(it) {
    return fetch(endpoint + "skip", {
      method: "POST",
      headers: { "X-CSRF-Token": token, "Content-Type": "application/x-www-form-urlencoded" },
      credentials: "same-origin",
      body: "path=" + encodeURIComponent(dest + "/" + it.rel)
    }).then(function () {}, function () {});
  }

  // The dialog is shared by up to MAX_PARALLEL Uploads: one question at a time.
  function showNextAsk() {
    if (asking || askQueue.length === 0) { return; }
    asking = askQueue.shift();
    askName.textContent = asking.rel;
    askText.textContent = asking.askText;
    askAll.checked = false;
    ask.hidden = false;
  }

  function dropAsk(it) {
    var i = askQueue.indexOf(it);
    if (i >= 0) { askQueue.splice(i, 1); }
    if (asking === it) { asking = null; ask.hidden = true; }
    it.onChoice = null;
    showNextAsk();
  }

  // Apply-to-all also answers the questions already waiting behind the dialog, once each.
  function answerQueued(choice) {
    askQueue.slice().forEach(function (q) {
      if (!q.autoTried) {
        q.autoTried = true;
        q.answer(choice);
      }
    });
  }

  function chooseConflict(it, text, onChoice) {
    // Apply-to-all answers by itself, but only once per file, so a choice that keeps
    // failing falls back to asking.
    if (allChoice && !it.autoTried) {
      it.autoTried = true;
      onChoice(allChoice);
      return;
    }
    setState(it, "asking", msg("conflict_wait"));
    it.cancel.hidden = false;
    it.askText = text;
    it.answer = function (choice) {
      dropAsk(it);
      onChoice(choice);
    };
    it.onChoice = function (choice) {
      if (askAll.checked) {
        allChoice = choice;
        answerQueued(choice);
      }
      it.answer(choice);
    };
    askQueue.push(it);
    showNextAsk();
  }

  // The server hears every choice, so the audit log shows it: a late one by resolve
  // (skip discards the kept data there), one made before any data was sent by /skip.
  function resolveLate(it, choice) {
    var skip = choice === "skip";
    setState(it, "running", msg(skip ? "waiting" : "uploading"));
    fetch(it.upload.url + "/resolve?choice=" + encodeURIComponent(choice), {
      method: "POST",
      headers: { "X-CSRF-Token": token, "Tus-Resumable": "1.0.0" },
      credentials: "same-origin"
    }).then(function (res) {
      if (it.state === "cancelled") { return null; }
      if (res.status === 204) {
        if (skip) { skipped(it); return null; }
        if (it.bar) { it.bar.value = 100; }
        settle(it, "done", msg("done"));
        return null;
      }
      return res.text().then(function (body) { conflicted(it, res.status, body, true); });
    }, function () {
      if (it.state === "cancelled") { return; }
      settle(it, "failed", msg("failed"));
    });
  }

  function conflicted(it, code, body, late) {
    if (code !== 409) {
      var f = failureText({ originalResponse: { getStatus: function () { return code; } } });
      settle(it, f[0], f[1]);
      return;
    }
    if (body.indexOf("ERR_CONFLICT_PARENT") >= 0) { // nothing is kept on the server for it
      settle(it, "exists", msg("parent_exists"));
      return;
    }
    if (body.indexOf("ERR_CONFLICT_FOLDER") >= 0) {
      if (late && it.upload) { it.upload.abort(true); }
      settle(it, "exists", msg("folder_exists"));
      return;
    }
    var text = body.indexOf("ERR_CONFLICT_TRASHED") >= 0 ? msg("conflict_trashed") : msg("conflict_prompt");
    chooseConflict(it, text, function (choice) {
      if (late) { resolveLate(it, choice); }
      else if (choice === "skip") { reportSkip(it).then(function () { skipped(it); }); }
      else { run(it, choice); }
    });
  }

  function waiting() {
    while (cursor < items.length && items[cursor].state !== "waiting") { cursor++; }
    return cursor < items.length ? items[cursor] : null;
  }

  function finish() {
    var ok = 0, bad = 0;
    for (var i = 0; i < items.length; i++) {
      if (items[i].state === "done") { ok++; } else if (items[i].state === "failed" || items[i].state === "exists") { bad++; }
    }
    cancelBtn.hidden = true;
    startBtn.disabled = false;
    var text = fmt(msg("summary"), [ok, bad]);
    if (skippedEmpty > 0) { text += " " + fmt(msg("empty_skipped"), [skippedEmpty]); }
    status.textContent = text;
    // A reload would wipe the report of what failed, so only reload when nothing did.
    if (bad === 0 && skippedEmpty === 0) {
      if (ok > 0) { window.location.reload(); }
    } else if (ok > 0) {
      status.textContent += " " + msg("reload_hint");
    }
  }

  // pump starts waiting Uploads up to MAX_PARALLEL and reports when everything is over.
  function pump() {
    var it;
    while (active < MAX_PARALLEL && (it = waiting())) {
      active++;
      run(it);
    }
    if (active === 0 && reading === 0 && !waiting() && items.length > 0 && startBtn.disabled) { finish(); }
  }

  // settle ends one Upload exactly once (an abort can also raise onError) and starts more.
  function settle(it, state, text) {
    if (it.settled) { return; }
    it.settled = true;
    setState(it, state, text);
    active--;
    refreshTotal();
    pump();
  }

  function run(it, choice) {
    setState(it, "running", msg("uploading"));
    var path = dest + "/" + it.rel;
    var md = { path: path };
    if (choice) { md.conflict = choice; }
    var up = new tus.Upload(it.file, {
      endpoint: endpoint,
      retryDelays: [0, 1000, 3000, 5000, 10000],
      headers: { "X-CSRF-Token": token },
      // A 409 (Conflict, for the User to settle) and a 507 (no room) are answers, not
      // outages: show them at once instead of retrying (tus-js-client retries 409, and a
      // folder of existing files would otherwise wait on each one). A mismatched offset is
      // the exception: a retry asks the server where the Upload stands.
      onShouldRetry: function (err, retryAttempt, options) {
        var res = err && err.originalResponse;
        var code = res ? res.getStatus() : 0;
        if ((code === 409 && !offsetMismatch(err)) || code === 507) { return false; }
        return tus.defaultOptions.onShouldRetry(err, retryAttempt, options);
      },
      metadata: md,
      // tus' default fingerprint is name + size + mtime, which two files in different
      // folders can share (empty files, copies); resuming the wrong one would put the
      // data at the wrong Path. The destination Path joins the fingerprint.
      fingerprint: function (file, options) {
        return Promise.resolve(["nas-files", path, file.size, file.lastModified, options.endpoint].join("|"));
      },
      onError: function (err) {
        if (it.state === "cancelled") { return; }
        var res = err && err.originalResponse;
        if (res && res.getStatus() === 409 && !offsetMismatch(err)) {
          conflicted(it, 409, conflictBody(err), isLate(err));
          return;
        }
        var f = failureText(err);
        settle(it, f[0], f[1]);
      },
      onProgress: function (sent, size) {
        it.sent = sent;
        if (it.bar) { it.bar.value = percent(sent, size); }
        setDetail(it);
        refreshTotal();
      },
      onSuccess: function () {
        if (it.bar) { it.bar.value = 100; }
        settle(it, "done", msg("done"));
      }
    });
    it.upload = up;
    // Resume an earlier attempt for the same file (stored in localStorage), if any.
    up.findPreviousUploads().then(function (prev) {
      if (prev.length > 0) { up.resumeFromPreviousUpload(prev[0]); }
      up.start();
    }, function () { up.start(); });
  }

  // add queues one file at its relative path ("name" or "Folder/sub/name").
  function add(file, rel, error) {
    rel = nfc(rel);
    if (isOSNoise(rel)) { return null; }
    var it = { file: file, rel: rel, row: null, bar: null, text: null, detail: null, cancel: null, upload: null, sent: 0, state: "waiting", settled: false };
    items.push(it);
    if (error) {
      it.settled = true;
      setState(it, "failed", msg("failed"));
    } else if (hiddenPath(rel)) {
      it.settled = true; // never sent: the server would refuse it as well
      setState(it, "failed", msg("bad_name"));
    } else {
      setState(it, "waiting", msg("waiting"));
    }
    return it;
  }

  function cancelItem(it) {
    var wasRunning = it.state === "running" || it.state === "asking";
    if (it.state === "asking") { dropAsk(it); }
    setState(it, "cancelled", msg("cancelled"));
    if (wasRunning && it.upload) {
      it.upload.abort(true); // true: also delete the partial (or kept) data on the server
      it.settled = false;
      settle(it, "cancelled", msg("cancelled"));
    }
    refreshTotal();
  }

  // begin queues files and starts working. A new selection while one runs joins it.
  function begin(picked, empty) {
    if (!startBtn.disabled) { // nothing running: start a fresh report
      items = [];
      cursor = 0;
      skippedEmpty = 0;
      list.innerHTML = "";
    }
    chosen = [];
    clearPicks();
    skippedEmpty += empty || 0;
    for (var i = 0; i < picked.length; i++) { add(picked[i].file, picked[i].rel, picked[i].error); }
    if (items.length === 0) {
      status.classList.remove("reading");
      status.textContent = skippedEmpty > 0 ? fmt(msg("empty_skipped"), [skippedEmpty]) : "";
      activity.hidden = true;
      return;
    }
    status.classList.remove("reading");
    status.textContent = "";
    activity.hidden = false;
    refreshTotal();
    allChoice = "";
    startBtn.disabled = true;
    cancelBtn.hidden = false;
    pump();
  }

  startBtn.addEventListener("click", function () {
    if (startBtn.disabled || chosen.length === 0) { return; }
    begin(chosen);
  });

  function picksChanged() {
    if (startBtn.disabled) { return; } // the running selection is not touched
    var i, f;
    chosen = [];
    var files = input.files || [];
    for (i = 0; i < files.length; i++) { chosen.push({ file: files[i], rel: files[i].name }); }
    files = folderInput && folderInput.files ? folderInput.files : [];
    for (i = 0; i < files.length; i++) {
      f = files[i];
      chosen.push({ file: f, rel: f.webkitRelativePath || f.name });
    }
    showChosen();
  }
  input.addEventListener("change", picksChanged);
  if (folderInput) { folderInput.addEventListener("change", picksChanged); }

  function pick(choice) { return function () { if (asking && asking.onChoice) { asking.onChoice(choice); } }; }
  document.getElementById("upload-conflict-skip").addEventListener("click", pick("skip"));
  document.getElementById("upload-conflict-keep").addEventListener("click", pick("keep"));
  document.getElementById("upload-conflict-replace").addEventListener("click", pick("replace"));

  cancelBtn.addEventListener("click", function () {
    var i;
    if (!startBtn.disabled) { // only a pick so far: drop it
      chosen = [];
      clearPicks();
      showChosen();
      return;
    }
    for (i = 0; i < items.length; i++) { // waiting ones first, so none starts meanwhile
      if (items[i].state === "waiting") { cancelItem(items[i]); }
    }
    for (i = 0; i < items.length; i++) {
      if (items[i].state === "running" || items[i].state === "asking") { cancelItem(items[i]); }
    }
  });

  /* ---- Drag and drop of files and folders ---- */

  // readAll collects every entry of a directory. A reader hands out one page at a time
  // (100 in Chrome) and signals the end with an empty page, so a folder of 10,000+
  // entries needs many calls.
  function readAll(reader) {
    return new Promise(function (resolve, reject) {
      var all = [];
      (function page() {
        reader.readEntries(function (chunk) {
          if (chunk.length === 0) { resolve(all); return; }
          for (var i = 0; i < chunk.length; i++) { all.push(chunk[i]); }
          page();
        }, reject);
      })();
    });
  }

  // walk adds the files below entry to found, one at a time so open handles stay few.
  function walk(entry, prefix, found) {
    if (entry.isFile) {
      return new Promise(function (resolve) {
        entry.file(function (f) {
          found.files.push({ file: f, rel: prefix + entry.name });
          resolve();
        }, function () {
          found.files.push({ file: null, rel: prefix + entry.name, error: true });
          resolve();
        });
      });
    }
    if (!entry.isDirectory) { return Promise.resolve(); }
    return readAll(entry.createReader()).then(function (children) {
      if (children.length === 0) { found.empty++; return null; }
      return children.reduce(function (p, child) {
        return p.then(function () { return walk(child, prefix + entry.name + "/", found); });
      }, Promise.resolve());
    }, function () {
      found.files.push({ file: null, rel: prefix + entry.name, error: true });
    });
  }

  function hasFiles(dt) {
    return !!dt && !!dt.types && Array.prototype.indexOf.call(dt.types, "Files") >= 0;
  }

  document.addEventListener("dragover", function (ev) {
    if (!hasFiles(ev.dataTransfer)) { return; }
    ev.preventDefault();
    document.body.classList.add("dragging");
  });
  document.addEventListener("dragleave", function (ev) {
    if (!ev.relatedTarget) { document.body.classList.remove("dragging"); }
  });

  document.addEventListener("drop", function (ev) {
    var dt = ev.dataTransfer;
    if (!hasFiles(dt)) { return; }
    ev.preventDefault();
    document.body.classList.remove("dragging");
    // Entries must be taken now: the DataTransfer is emptied once this handler returns.
    var entries = [], plain = [], i, e;
    if (dt.items && dt.items.length && dt.items[0].webkitGetAsEntry) {
      for (i = 0; i < dt.items.length; i++) {
        if (dt.items[i].kind !== "file") { continue; }
        e = dt.items[i].webkitGetAsEntry();
        if (e) { entries.push(e); } else if (dt.items[i].getAsFile()) { plain.push(dt.items[i].getAsFile()); }
      }
    } else {
      for (i = 0; i < dt.files.length; i++) { plain.push(dt.files[i]); }
    }
    var found = { files: [], empty: 0 };
    for (i = 0; i < plain.length; i++) { found.files.push({ file: plain[i], rel: plain[i].name }); }
    reading++;
    if (startBtn.disabled === false) {
      status.textContent = msg("reading");
      status.classList.add("reading");
    }
    entries.reduce(function (p, entry) {
      return p.then(function () { return walk(entry, "", found); });
    }, Promise.resolve()).then(function () {
      reading--;
      begin(found.files, found.empty);
    });
  });
})();
