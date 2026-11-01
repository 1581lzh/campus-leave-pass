/* Layout animation layer for the campus leave pass UI.
 *
 * Inspired by anime.js `createLayout()` (FLIP: First, Last, Invert, Play) but
 * built on the Web Animations API so the app stays dependency-free,
 * CSP-compatible and inside the single Go binary. The helpers only animate
 * transform / opacity / height, so they never change DOM structure. Every
 * rectangle is measured before any transform is written, which keeps the
 * whole transition inside one layout pass instead of thrashing.
 *
 * Progressive enhancement: without JavaScript, without WAAPI or with
 * prefers-reduced-motion the DOM changes instantly and the UI stays usable.
 */
(function () {
  "use strict";

  var reduceQuery = window.matchMedia
    ? window.matchMedia("(prefers-reduced-motion: reduce)")
    : null;
  var reduced = !!(reduceQuery && reduceQuery.matches);

  var supportsWAAPI =
    typeof Element !== "undefined" &&
    typeof Element.prototype.animate === "function";

  /* Keep in sync with motion.js and the --motion-ease custom property. */
  var EASE_OUT = "cubic-bezier(0.22, 1, 0.36, 1)";
  var EASE_IN = "cubic-bezier(0.4, 0, 1, 1)";

  function canAnimate() {
    return !reduced && supportsWAAPI;
  }

  function stop(animation) {
    if (!animation) return;
    try {
      animation.cancel();
    } catch (err) {
      /* no-op */
    }
  }

  function measure(el) {
    var rect = el.getBoundingClientRect();
    return { x: rect.left, y: rect.top };
  }

  function collect(root, selector) {
    var found = root.querySelectorAll(selector);
    var out = [];
    for (var i = 0; i < found.length; i++) out.push(found[i]);
    return out;
  }

  function saveStyles(el, names) {
    var saved = {};
    for (var i = 0; i < names.length; i++) saved[names[i]] = el.style[names[i]];
    return saved;
  }

  function restoreStyles(el, saved) {
    for (var name in saved) {
      if (Object.prototype.hasOwnProperty.call(saved, name)) {
        el.style[name] = saved[name] || "";
      }
    }
  }

  /* Only one transform/opacity animation per element may be active at a time;
     a newer feedback animation replaces the older one instead of stacking. */
  var elementAnimations =
    typeof WeakMap === "function" ? new WeakMap() : null;

  /* Start one WAAPI animation and always restore the authored inline styles
     afterwards, so a dropped animation event cannot leave a stale transform
     or a frozen opacity behind. */
  function animateElement(el, frames, options, active) {
    if (elementAnimations) {
      var previous = elementAnimations.get(el);
      if (previous) previous.cleanup();
    }

    var saved = saveStyles(el, ["transform", "opacity"]);
    var animation;
    try {
      animation = el.animate(frames, {
        duration: options.duration,
        delay: options.delay || 0,
        easing: options.easing || EASE_OUT,
        fill: "both"
      });
    } catch (err) {
      return null;
    }

    var entry = { el: el, animation: animation, done: false };
    entry.cleanup = function () {
      if (entry.done) return;
      entry.done = true;
      stop(animation);
      restoreStyles(el, saved);
      if (elementAnimations && elementAnimations.get(el) === entry) {
        elementAnimations.delete(el);
      }
    };
    animation.onfinish = entry.cleanup;
    animation.oncancel = entry.cleanup;
    window.setTimeout(
      entry.cleanup,
      options.duration + (options.delay || 0) + 200
    );
    if (elementAnimations) elementAnimations.set(el, entry);
    if (active) active.push(entry);
    return entry;
  }

  function cancelAll(active) {
    for (var i = 0; i < active.length; i++) active[i].cleanup();
    active.length = 0;
  }

  /* ---- FLIP layout transitions -------------------------------------- */
  function createLayout(root, options) {
    if (!root) return null;
    var opts = options || {};
    var selector = opts.children || "*";
    var duration = opts.duration == null ? 360 : opts.duration;
    var ease = opts.ease || EASE_OUT;
    var enterFrom = opts.enterFrom || null;
    var enterDuration =
      opts.enterDuration == null
        ? Math.round(duration * 0.8)
        : opts.enterDuration;
    var states = null;
    var active = [];

    function record() {
      cancelAll(active);
      var elements = collect(root, selector);
      states = [];
      for (var i = 0; i < elements.length; i++) {
        states.push({ el: elements[i], box: measure(elements[i]) });
      }
    }

    function enter(el) {
      var from = {};
      var to = {};
      if (enterFrom.opacity != null) {
        from.opacity = String(enterFrom.opacity);
        to.opacity = "1";
      }
      if (enterFrom.transform) {
        from.transform = enterFrom.transform;
        to.transform = "translate3d(0, 0, 0)";
      }
      if (!from.opacity && !from.transform) return;
      animateElement(
        el,
        [from, to],
        { duration: enterDuration, easing: ease },
        active
      );
    }

    function animate() {
      var previous = states;
      states = null;
      if (!previous) return;
      cancelAll(active);

      /* Reduced motion / no WAAPI: the DOM mutation has already happened and
         the new layout is the final state, so there is nothing to play. */
      if (!canAnimate()) return;

      /* Read phase: measure every element before writing any transform. */
      var current = collect(root, selector);
      var last = [];
      for (var i = 0; i < current.length; i++) {
        last.push({ el: current[i], box: measure(current[i]) });
      }

      var first = new Map();
      for (var j = 0; j < previous.length; j++) {
        first.set(previous[j].el, previous[j].box);
      }

      /* Write phase. */
      for (var k = 0; k < last.length; k++) {
        var item = last[k];
        var before = first.get(item.el);
        if (!before) {
          if (enterFrom) enter(item.el);
          continue;
        }
        var dx = before.x - item.box.x;
        var dy = before.y - item.box.y;
        if (Math.abs(dx) < 0.5 && Math.abs(dy) < 0.5) continue;
        animateElement(
          item.el,
          [
            {
              transform:
                "translate3d(" +
                dx.toFixed(2) +
                "px," +
                dy.toFixed(2) +
                "px,0)"
            },
            { transform: "translate3d(0, 0, 0)" }
          ],
          { duration: duration, easing: ease },
          active
        );
      }
    }

    function update(mutate) {
      record();
      if (typeof mutate === "function") mutate();
      animate();
    }

    function revert() {
      cancelAll(active);
      states = null;
    }

    return {
      root: root,
      record: record,
      animate: animate,
      update: update,
      revert: revert
    };
  }

  /* ---- Container size transitions ----------------------------------- */
  var sizeActive = [];

  function cancelSizeFor(el) {
    for (var i = sizeActive.length - 1; i >= 0; i--) {
      if (sizeActive[i].el === el) {
        sizeActive[i].cleanup();
        sizeActive.splice(i, 1);
      }
    }
  }

  function trackSize(el, animation, saved, done) {
    var entry = {
      el: el,
      animation: animation,
      done: false,
      settled: false
    };

    entry.cleanup = function () {
      if (entry.done) return;
      entry.done = true;
      stop(animation);
      restoreStyles(el, saved);
    };

    function finish() {
      if (entry.settled) return;
      entry.settled = true;
      entry.cleanup();
      var index = sizeActive.indexOf(entry);
      if (index >= 0) sizeActive.splice(index, 1);
      if (done) done();
    }

    animation.onfinish = finish;
    animation.oncancel = entry.cleanup;
    window.setTimeout(finish, 3000);
    sizeActive.push(entry);
  }

  function startSize(el, setup, frames, duration, easing, done) {
    var saved = saveStyles(el, ["height", "overflow"]);
    if (setup) setup();

    var animation;
    try {
      animation = el.animate(frames, {
        duration: duration,
        easing: easing || EASE_OUT,
        fill: "both"
      });
    } catch (err) {
      restoreStyles(el, saved);
      if (done) done();
      return;
    }

    trackSize(el, animation, saved, done);
  }

  function expand(el, done) {
    if (!el) {
      if (done) done();
      return;
    }
    cancelSizeFor(el);
    if (!canAnimate()) {
      if (done) done();
      return;
    }

    var target = el.offsetHeight;
    if (!target) {
      if (done) done();
      return;
    }

    startSize(
      el,
      function () {
        el.style.overflow = "hidden";
        el.style.height = "0px";
        void el.offsetHeight;
      },
      [{ height: "0px" }, { height: target + "px" }],
      260,
      EASE_OUT,
      done
    );
  }

  function collapse(el, done) {
    if (!el) {
      if (done) done();
      return;
    }
    cancelSizeFor(el);
    if (!canAnimate()) {
      if (done) done();
      return;
    }

    var from = el.offsetHeight;
    if (!from) {
      if (done) done();
      return;
    }

    startSize(
      el,
      function () {
        el.style.overflow = "hidden";
        el.style.height = from + "px";
        void el.offsetHeight;
      },
      [{ height: from + "px" }, { height: "0px" }],
      200,
      EASE_IN,
      done
    );
  }

  /* Animate a container from its current height to the height produced by
     `mutate()`. Elements that the mutation reveals (selector `enter`) fade in
     at the same time, so a form field swap reads as one movement. */
  function morph(el, mutate, options) {
    if (!el || typeof mutate !== "function") return;
    cancelSizeFor(el);
    if (!canAnimate()) {
      mutate();
      return;
    }

    var opts = options || {};
    var duration = opts.duration == null ? 240 : opts.duration;
    var saved = saveStyles(el, ["height", "overflow"]);
    var before = el.offsetHeight;

    var reveal = [];
    if (opts.enter) {
      var candidates = el.querySelectorAll(opts.enter);
      for (var i = 0; i < candidates.length; i++) {
        if (candidates[i].hidden) reveal.push(candidates[i]);
      }
    }

    el.style.overflow = "hidden";
    el.style.height = before + "px";
    mutate();

    /* Measure the natural target height, then restore the fixed start height
       before the animation begins. */
    el.style.height = "auto";
    var after = el.offsetHeight;
    el.style.height = before + "px";
    void el.offsetHeight;

    for (var j = 0; j < reveal.length; j++) {
      if (!reveal[j].hidden) {
        animateElement(
          reveal[j],
          [
            { opacity: "0", transform: "translate3d(0, 4px, 0)" },
            { opacity: "1", transform: "translate3d(0, 0, 0)" }
          ],
          { duration: 200, easing: EASE_OUT },
          null
        );
      }
    }

    if (Math.abs(after - before) < 1) {
      restoreStyles(el, saved);
      return;
    }

    var animation;
    try {
      animation = el.animate(
        [{ height: before + "px" }, { height: after + "px" }],
        { duration: duration, easing: EASE_OUT, fill: "both" }
      );
    } catch (err) {
      restoreStyles(el, saved);
      return;
    }

    trackSize(el, animation, saved, null);
  }

  function reset(el) {
    if (!el) return;
    cancelSizeFor(el);
  }

  if (reduceQuery) {
    var onMotionChange = function () {
      reduced = reduceQuery.matches;
    };
    if (typeof reduceQuery.addEventListener === "function") {
      reduceQuery.addEventListener("change", onMotionChange);
    } else if (typeof reduceQuery.addListener === "function") {
      reduceQuery.addListener(onMotionChange);
    }
  }

  window.LPLayout = {
    enabled: canAnimate,
    createLayout: createLayout,
    expand: expand,
    collapse: collapse,
    morph: morph,
    reset: reset
  };
})();
