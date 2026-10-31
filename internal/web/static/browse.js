/* Folder page niceties. Everything here is optional: the selection form and the
   create-folder <details> work with JavaScript off. */
(function () {
  "use strict";
  var count = document.getElementById("selected-count");
  if (count) {
    var boxes = document.querySelectorAll('input[name="names"]');
    var refresh = function () {
      var n = document.querySelectorAll('input[name="names"]:checked').length;
      count.textContent = n > 0 ? count.getAttribute("data-msg").replace("%d", String(n)) : "";
    };
    for (var i = 0; i < boxes.length; i++) { boxes[i].addEventListener("change", refresh); }
    refresh(); // a reload keeps the ticks
  }

  var pop = document.querySelector("details.popover");
  if (pop) {
    document.addEventListener("click", function (ev) {
      if (pop.open && !pop.contains(ev.target)) { pop.open = false; }
    });
    document.addEventListener("keydown", function (ev) {
      if (ev.key !== "Escape" || !pop.open) { return; }
      var inside = pop.contains(document.activeElement);
      pop.open = false;
      if (inside) { pop.querySelector("summary").focus(); }
    });
  }
})();
