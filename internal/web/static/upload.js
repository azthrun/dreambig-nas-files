/* Upload panel on a folder page: progressive enhancement over tus-js-client (vendored,
   no CDN). Without JavaScript the panel stays hidden and browsing works as before.
   Uses no secure-context API (the site is plain HTTP, ADR-0002): no Web Crypto,
   service workers or File System Access API. Folders come from the <input
   webkitdirectory> picker or from dropped DataTransferItem.webkitGetAsEntry() entries.
   Every file is its own tus Upload at dest + "/" + its relative path; the server creates
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

  function percent(a, b) { return b > 0 ? Math.floor((a * 100) / b) : 100; }

  // Names are sent in NFC (macOS file systems hand out NFD); the server normalises too.
  function nfc(s) { return s.normalize ? s.normalize("NFC") : s; }

  // Mirrors mounts.IsHiddenName: DSM and service folders are never uploaded.
  function isHidden(name) {
    var n = name.toLowerCase();
    return name.charAt(0) === "@" || name.charAt(0) === "#" || n === ".ds_store" ||
      n === ".nas-files-staging" || n === ".nas-files-trash" || n === ".nas-mcp-trash";
  }

  function hiddenPath(rel) {
    var segs = rel.split("/");
    for (var i = 0; i < segs.length; i++) { if (isHidden(segs[i])) { return true; } }
    return false;
  }

  // Rows are made lazily: a big folder has thousands of waiting files and only the ones
  // that run or fail need a line on the page.
  function ensureRow(it) {
    if (it.row) { return; }
    var row = document.createElement("li");
    var name = document.createElement("span");
    name.className = "upload-name";
    name.textContent = it.rel;
    var bar = document.createElement("progress");
    bar.max = 100;
    bar.value = 0;
    var text = document.createElement("span");
    text.className = "upload-state";
    var cancel = document.createElement("button");
    cancel.type = "button";
    cancel.textContent = msg("cancel");
    cancel.addEventListener("click", function () { cancelItem(it); });
    row.appendChild(name);
    row.appendChild(bar);
    row.appendChild(text);
    row.appendChild(cancel);
    list.appendChild(row);
    it.row = row; it.bar = bar; it.text = text; it.cancel = cancel;
  }

  function setState(it, state, text) {
    it.state = state;
    if (state !== "waiting") { ensureRow(it); }
    if (it.row) {
      it.text.textContent = text;
      it.cancel.hidden = state === "done" || state === "failed" || state === "cancelled" || state === "exists";
      if (state === "done" && items.length > KEEP_ROWS) {
        list.removeChild(it.row);
        it.row = null;
      }
    }
  }

  function refreshTotal() {
    var sent = 0, size = 0;
    for (var i = 0; i < items.length; i++) {
      size += items[i].file ? items[i].file.size : 0;
      sent += items[i].state === "done" && items[i].file ? items[i].file.size : items[i].sent;
    }
    var p = percent(sent, size);
    total.value = p;
    totalText.textContent = p + "%";
  }

  function failureText(err) {
    var res = err && err.originalResponse;
    var code = res ? res.getStatus() : 0;
    if (code === 409) { return ["exists", msg("exists")]; }
    if (code === 403 || code === 404) { return ["failed", msg("refused")]; }
    if (code === 400) { return ["failed", msg("bad_name")]; }
    return ["failed", msg("failed")];
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

  function run(it) {
    setState(it, "running", msg("uploading"));
    var path = dest + "/" + it.rel;
    var up = new tus.Upload(it.file, {
      endpoint: endpoint,
      retryDelays: [0, 1000, 3000, 5000, 10000],
      // A Conflict (409) is an answer, not a glitch: report it instead of retrying.
      onShouldRetry: function (err) {
        var res = err && err.originalResponse;
        var code = res ? res.getStatus() : 0;
        return code === 0 || code >= 500 || code === 423;
      },
      headers: { "X-CSRF-Token": token },
      metadata: { path: path },
      // tus' default fingerprint is name + size + mtime, which two files in different
      // folders can share (empty files, copies); resuming the wrong one would put the
      // data at the wrong Path. The destination Path joins the fingerprint.
      fingerprint: function (file, options) {
        return Promise.resolve(["nas-files", path, file.size, file.lastModified, options.endpoint].join("|"));
      },
      onError: function (err) {
        if (it.state === "cancelled") { return; }
        var f = failureText(err);
        settle(it, f[0], f[1]);
      },
      onProgress: function (sent, size) {
        it.sent = sent;
        if (it.bar) { it.bar.value = percent(sent, size); }
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
    var it = { file: file, rel: rel, row: null, bar: null, text: null, cancel: null, upload: null, sent: 0, state: "waiting", settled: false };
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
    var wasRunning = it.state === "running";
    setState(it, "cancelled", msg("cancelled"));
    if (wasRunning && it.upload) {
      it.upload.abort(true); // true: also delete the partial data on the server
      it.settled = false;
      settle(it, "cancelled", msg("cancelled"));
    }
    refreshTotal();
  }

  // begin queues files and starts working. A new batch while one runs joins it.
  function begin(batch, empty) {
    if (!startBtn.disabled) { // nothing running: start a fresh report
      items = [];
      cursor = 0;
      skippedEmpty = 0;
      list.innerHTML = "";
    }
    skippedEmpty += empty || 0;
    for (var i = 0; i < batch.length; i++) { add(batch[i].file, batch[i].rel, batch[i].error); }
    if (items.length === 0) {
      status.textContent = skippedEmpty > 0 ? fmt(msg("empty_skipped"), [skippedEmpty]) : "";
      return;
    }
    status.textContent = "";
    refreshTotal();
    startBtn.disabled = true;
    cancelBtn.hidden = false;
    pump();
  }

  startBtn.addEventListener("click", function () {
    if (startBtn.disabled) { return; }
    var batch = [], i, f;
    var files = input.files || [];
    for (i = 0; i < files.length; i++) { batch.push({ file: files[i], rel: files[i].name }); }
    files = folderInput && folderInput.files ? folderInput.files : [];
    for (i = 0; i < files.length; i++) {
      f = files[i];
      batch.push({ file: f, rel: f.webkitRelativePath || f.name });
    }
    begin(batch);
  });

  cancelBtn.addEventListener("click", function () {
    var i;
    for (i = 0; i < items.length; i++) { // waiting ones first, so none starts meanwhile
      if (items[i].state === "waiting") { cancelItem(items[i]); }
    }
    for (i = 0; i < items.length; i++) {
      if (items[i].state === "running") { cancelItem(items[i]); }
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
        reader.readEntries(function (batch) {
          if (batch.length === 0) { resolve(all); return; }
          for (var i = 0; i < batch.length; i++) { all.push(batch[i]); }
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
    panel.classList.add("dragging");
  });
  document.addEventListener("dragleave", function (ev) {
    if (!ev.relatedTarget) { panel.classList.remove("dragging"); }
  });

  document.addEventListener("drop", function (ev) {
    var dt = ev.dataTransfer;
    if (!hasFiles(dt)) { return; }
    ev.preventDefault();
    panel.classList.remove("dragging");
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
    if (startBtn.disabled === false) { status.textContent = msg("reading"); }
    entries.reduce(function (p, entry) {
      return p.then(function () { return walk(entry, "", found); });
    }, Promise.resolve()).then(function () {
      reading--;
      begin(found.files, found.empty);
    });
  });
})();
