// Landing-page behavior: install-command copy button + the live slack widget.
// The widget runs the real headroom formula over a toy node so the hero chart
// is the product, not a picture of it. Values mirror docs/design.md §5 with
// floor/caps/hysteresis omitted (the caption says so).

(function () {
  "use strict";

  // ---- copy button ----
  var copyBtn = document.getElementById("copy-btn");
  var cmd = document.getElementById("install-cmd");
  if (copyBtn && cmd && navigator.clipboard) {
    copyBtn.addEventListener("click", function () {
      navigator.clipboard.writeText(cmd.textContent).then(function () {
        copyBtn.textContent = "COPIED";
        setTimeout(function () { copyBtn.textContent = "COPY"; }, 1600);
      });
    });
  }

  // ---- live slack widget ----
  var ALLOCATABLE = 4.0; // cores
  var PODS = [
    { name: "api", request: 1.0 },
    { name: "worker", request: 0.5 },
    { name: "sidecar", request: 0.25 },
  ];
  var MANAGED_SUM = PODS.reduce(function (s, p) { return s + p.request; }, 0);

  var chart = document.getElementById("chart");
  var slider = document.getElementById("booking");
  var roSlack = document.getElementById("ro-slack");
  var roMult = document.getElementById("ro-mult");
  if (!chart || !slider) return;

  var CHART_TOP = 14; // px reserved above the ceiling line

  // Build one column per pod: headroom extension stacked on the request bar.
  var bars = PODS.map(function (p) {
    var bar = document.createElement("div");
    bar.className = "bar";
    var head = document.createElement("div");
    head.className = "head";
    var req = document.createElement("div");
    req.className = "req";
    var tag = document.createElement("div");
    tag.className = "tag";
    bar.appendChild(head);
    bar.appendChild(req);
    bar.appendChild(tag);
    chart.appendChild(bar);
    return { pod: p, head: head, req: req, tag: tag };
  });

  function render() {
    var unmanaged = parseFloat(slider.value);
    var booked = MANAGED_SUM + unmanaged;
    var slack = Math.max(0, ALLOCATABLE - booked);
    var mult = 1 + slack / MANAGED_SUM;

    var usable = chart.clientHeight - CHART_TOP - 24; // room below ceiling, minus tag row
    var perCore = usable / ALLOCATABLE;

    bars.forEach(function (b) {
      var limit = b.pod.request * mult;
      var reqPx = Math.max(3, b.pod.request * perCore);
      var headPx = Math.max(0, (limit - b.pod.request) * perCore);
      b.req.style.height = reqPx + "px";
      b.head.style.height = headPx + "px";
      b.tag.innerHTML =
        b.pod.name + " " + fmt(b.pod.request) + "→<b>" + fmt(limit) + "</b>";
    });

    roSlack.textContent = fmt(slack) + " CPU";
    roMult.textContent = "×" + mult.toFixed(2);
  }

  function fmt(cores) {
    return (Math.round(cores * 100) / 100).toString().replace(/^0\./, ".");
  }

  slider.addEventListener("input", render);
  window.addEventListener("resize", render);
  render();
})();
