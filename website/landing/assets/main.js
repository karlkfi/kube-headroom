// Landing-page behavior: install-command copy button + the live slack widget.
// The widget runs the real headroom formula over a toy node so the hero chart
// is the product, not a picture of it. +1/−1 POD emulate scheduling events —
// exactly the trigger the real controller recomputes on. Values mirror
// docs/design.md §5 with floor/caps/hysteresis omitted (the caption says so).

(function () {
  "use strict";

  // ---- progressive glow (html.fx) ----
  // Re-enable the SVG blur glow only where it's demonstrably cheap: hardware
  // WebGL (a software renderer string means GPU rendering is off — the exact
  // machines the blur chugged on), a reasonable core count, no data-saver,
  // and no reduced-motion preference. Any failure leaves the halo fallback.
  (function enableGlowIfCapable() {
    try {
      if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;
      if ((navigator.hardwareConcurrency || 0) < 4) return;
      if (navigator.deviceMemory && navigator.deviceMemory < 8) return;
      if (navigator.connection && navigator.connection.saveData) return;
      var canvas = document.createElement("canvas");
      var gl = canvas.getContext("webgl2") || canvas.getContext("webgl");
      if (!gl) return;
      var dbg = gl.getExtension("WEBGL_debug_renderer_info");
      var renderer = dbg
        ? String(gl.getParameter(dbg.UNMASKED_RENDERER_WEBGL))
        : "";
      if (/swiftshader|llvmpipe|softpipe|software|basic render/i.test(renderer)) return;
      document.documentElement.classList.add("fx");
    } catch (e) {
      /* stay on the cheap path */
    }
  })();

  // footer footnote: which signal path did the glow detector pick?
  var fxNote = document.getElementById("fx-note");
  if (fxNote && document.documentElement.classList.contains("fx")) {
    fxNote.textContent = "· SIGNAL HD · GPU GLOW ON";
  }

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
  var SIZES = [0.25, 0.25, 0.5, 0.5, 0.75, 1.0]; // weighted toward small pods
  var NAMES = ["api", "worker", "sidecar", "cache", "cron", "web", "batch",
    "queue", "proxy", "ingest", "auth", "etl", "logs", "feed", "sync", "jobs"];

  var chart = document.getElementById("chart");
  var addBtn = document.getElementById("pod-add");
  var delBtn = document.getElementById("pod-del");
  var hint = document.getElementById("ctrl-hint");
  var roSlack = document.getElementById("ro-slack");
  var roMult = document.getElementById("ro-mult");
  if (!chart || !addBtn || !delBtn) return;

  var CHART_TOP = 14; // px reserved above the ceiling line
  var pods = []; // { name, request, el: { bar, head, req, tag } }
  var nameIdx = 0;

  function requestSum() {
    return pods.reduce(function (s, p) { return s + p.request; }, 0);
  }

  function makeBar() {
    var bar = document.createElement("div");
    bar.className = "bar";
    var head = document.createElement("div");
    head.className = "head";
    var req = document.createElement("div");
    req.className = "req";
    var tag = document.createElement("div");
    tag.className = "tag";
    head.style.height = "0px";
    req.style.height = "0px";
    bar.appendChild(head);
    bar.appendChild(req);
    bar.appendChild(tag);
    chart.appendChild(bar);
    return { bar: bar, head: head, req: req, tag: tag };
  }

  function schedulePod(request) {
    var pod = {
      name: NAMES[nameIdx++ % NAMES.length],
      request: request,
      el: makeBar(),
    };
    pods.push(pod);
    return pod;
  }

  function addRandomPod() {
    var slack = ALLOCATABLE - requestSum();
    var fits = SIZES.filter(function (s) { return s <= slack + 1e-9; });
    if (!fits.length) return;
    var pod = schedulePod(fits[Math.floor(Math.random() * fits.length)]);
    // flush layout so the bar's 0-height start is committed, then render —
    // the height transition animates 0 → target without needing rAF
    void pod.el.bar.offsetHeight;
    render();
  }

  function evictPod() {
    if (pods.length <= 1) return;
    var p = pods.pop();
    chart.removeChild(p.el.bar);
    render();
  }

  function render() {
    var sum = requestSum();
    var slack = Math.max(0, ALLOCATABLE - sum);
    var mult = 1 + slack / sum;

    var usable = chart.clientHeight - CHART_TOP - 24; // below ceiling, minus tag row
    var perCore = usable / ALLOCATABLE;

    var compact = pods.length > 4; // narrow bars: drop names, keep the numbers
    pods.forEach(function (p) {
      var limit = p.request * mult;
      p.el.req.style.height = Math.max(3, p.request * perCore) + "px";
      p.el.head.style.height = Math.max(0, (limit - p.request) * perCore) + "px";
      p.el.tag.innerHTML = (compact ? "" : p.name + " ") +
        fmt(p.request) + "→<b>" + fmt(limit) + "</b>";
    });

    roSlack.textContent = fmt(slack) + " CPU";
    roMult.textContent = "×" + mult.toFixed(2);

    var minSize = Math.min.apply(null, SIZES);
    var nodeFull = slack < minSize - 1e-9;
    addBtn.disabled = nodeFull;
    delBtn.disabled = pods.length <= 1;
    if (hint) {
      hint.textContent = nodeFull
        ? "node full — every limit sits at its request"
        : "every scheduling event recomputes every limit";
      hint.classList.toggle("full", nodeFull);
    }
  }

  function fmt(cores) {
    return (Math.round(cores * 100) / 100).toString().replace(/^0\./, ".");
  }

  addBtn.addEventListener("click", addRandomPod);
  delBtn.addEventListener("click", evictPod);
  window.addEventListener("resize", render);

  // starting state: three pods, deterministic (randomness begins with +1 POD)
  [1.0, 0.5, 0.25].forEach(schedulePod);
  render();
})();
