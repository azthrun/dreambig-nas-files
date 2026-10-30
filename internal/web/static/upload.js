/* Upload panel on a folder page: progressive enhancement over tus-js-client (vendored,
   no CDN). Without JavaScript the panel stays hidden and browsing works as before.
   Uses no secure-context API (the site is plain HTTP, ADR-0002): no Web Crypto,
   service workers or File System Access API. Texts come from data-* attributes the
   server renders in the User's language. Browser behaviour is checked by hand. */
(function () {
  "use strict";
  var panel = document.getElementById("upload");
  if (!panel || !window.tus || !window.File) { return; }
  panel.hidden = false;

  var meta = document.querySelector('meta[name="csrf-token"]');
  var token = meta ? meta.getAttribute("content") : "";
  var endpoint = panel.getAttribute("data-endpoint");
  var dest = panel.getAttribute("data-dest");
  var input = document.getElementById("upload-files");
  var startBtn = document.getElementById("upload-start");
  var cancelBtn = document.getElementById("upload-cancel");
  var list = document.getElementById("upload-list");
  var total = document.getElementById("upload-total");
  var totalText = document.getElementById("upload-total-text");
  var status = document.getElementById("upload-status");

  function msg(key) { return panel.getAttribute("data-msg-" + key) || ""; }

  var items = [];   // {file, row, bar, text, cancel, upload, sent, state}
  var running = false;
  var current = -1;

  function percent(a, b) { return b > 0 ? Math.floor((a * 100) / b) : 100; }

  function setState(it, state, text) {
    it.state = state;
    it.text.textContent = text;
    it.cancel.hidden = state === "done" || state === "failed" || state === "cancelled" || state === "exists";
  }

  function refreshTotal() {
    var sent = 0, size = 0;
    for (var i = 0; i < items.length; i++) {
      size += items[i].file.size;
      sent += items[i].state === "done" ? items[i].file.size : items[i].sent;
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

  function next() {
    current++;
    while (current < items.length && items[current].state !== "waiting") { current++; }
    if (current >= items.length) {
      running = false;
      cancelBtn.hidden = true;
      startBtn.disabled = false;
      var ok = 0;
      for (var i = 0; i < items.length; i++) { if (items[i].state === "done") { ok++; } }
      status.textContent = msg("finished");
      if (ok > 0) { window.location.reload(); }
      return;
    }
    run(items[current]);
  }

  function run(it) {
    setState(it, "running", msg("uploading"));
    var up = new tus.Upload(it.file, {
      endpoint: endpoint,
      retryDelays: [0, 1000, 3000, 5000, 10000],
      headers: { "X-CSRF-Token": token },
      metadata: { path: dest + "/" + it.file.name },
      onError: function (err) {
        if (it.state === "cancelled") { return; }
        var f = failureText(err);
        setState(it, f[0], f[1]);
        next();
      },
      onProgress: function (sent, size) {
        it.sent = sent;
        it.bar.value = percent(sent, size);
        refreshTotal();
      },
      onSuccess: function () {
        it.bar.value = 100;
        setState(it, "done", msg("done"));
        refreshTotal();
        next();
      }
    });
    it.upload = up;
    // Resume an earlier attempt for the same file (stored in localStorage), if any.
    up.findPreviousUploads().then(function (prev) {
      if (prev.length > 0) { up.resumeFromPreviousUpload(prev[0]); }
      up.start();
    }, function () { up.start(); });
  }

  function add(file) {
    var row = document.createElement("li");
    var name = document.createElement("span");
    name.className = "upload-name";
    name.textContent = file.name;
    var bar = document.createElement("progress");
    bar.max = 100;
    bar.value = 0;
    var text = document.createElement("span");
    text.className = "upload-state";
    var cancel = document.createElement("button");
    cancel.type = "button";
    cancel.textContent = msg("cancel");
    row.appendChild(name);
    row.appendChild(bar);
    row.appendChild(text);
    row.appendChild(cancel);
    list.appendChild(row);
    var it = { file: file, row: row, bar: bar, text: text, cancel: cancel, upload: null, sent: 0, state: "waiting" };
    cancel.addEventListener("click", function () { cancelItem(it); });
    setState(it, "waiting", msg("waiting"));
    items.push(it);
  }

  function cancelItem(it) {
    var wasRunning = it.state === "running";
    setState(it, "cancelled", msg("cancelled"));
    if (wasRunning && it.upload) {
      it.upload.abort(true); // true: also delete the partial data on the server
      next();
    }
    refreshTotal();
  }

  startBtn.addEventListener("click", function () {
    if (running) { return; }
    items = [];
    list.innerHTML = "";
    var files = input.files || [];
    for (var i = 0; i < files.length; i++) { add(files[i]); }
    if (items.length === 0) { return; }
    status.textContent = "";
    refreshTotal();
    running = true;
    current = -1;
    startBtn.disabled = true;
    cancelBtn.hidden = false;
    next();
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
})();
