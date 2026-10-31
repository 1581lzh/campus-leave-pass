/* Motion layer for the campus leave pass UI.
 *
 * Inspired by anime.js (stagger, timelines, spring-like easing) but built on
 * the Web Animations API so the app stays dependency-free, CSP-compatible and
 * inside the single Go binary. Every effect is progressive enhancement:
 * without JavaScript, without WAAPI or with prefers-reduced-motion the UI is
 * fully usable and simply stays still.
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

  var EASE_OUT = "cubic-bezier(0.22, 1, 0.36, 1)";
  var EASE_IN = "cubic-bezier(0.4, 0, 1, 1)";
  var EASE_SPRING = "cubic-bezier(0.34, 1.56, 0.64, 1)";

  function canAnimate() {
    return !reduced && supportsWAAPI;
  }

  function animate(el, frames, options) {
    if (!canAnimate() || !el || typeof el.animate !== "function") return null;
    options = options || {};
    var opts = {
      duration: options.duration == null ? 420 : options.duration,
      delay: options.delay || 0,
      easing: options.easing || EASE_OUT,
      fill: options.fill || "backwards",
      iterations: options.iterations || 1
    };
    try {
      var animation = el.animate(frames, opts);
      if (options.hold) return animation;

      var cleanup = function () {
        try {
          animation.cancel();
        } catch (err) {
          /* no-op */
        }
      };
      animation.onfinish = cleanup;
      window.setTimeout(cleanup, opts.duration + opts.delay + 120);
      return animation;
    } catch (err) {
      return null;
    }
  }

  /* Run `callback` once an animation settles, with a safety timeout so a
     dropped animation event can never leave a dialog or toast stuck. */
  function settle(animation, callback) {
    var done = false;
    function finish() {
      if (done) return;
      done = true;
      callback();
    }
    if (!animation) {
      finish();
      return;
    }
    animation.onfinish = finish;
    animation.oncancel = finish;
    window.setTimeout(finish, 2000);
  }

  function cancel(animation) {
    if (!animation) return;
    try {
      animation.cancel();
    } catch (err) {
      /* no-op */
    }
  }

  /* ---- Number counters ---------------------------------------------- */
  function countUp(el, delay) {
    if (!canAnimate() || !el || typeof window.requestAnimationFrame !== "function") {
      return;
    }
    var raw = (el.textContent || "").trim();
    var match = /^(\d+)(%?)$/.exec(raw);
    if (!match) return;
    var target = parseInt(match[1], 10);
    if (!isFinite(target) || target <= 0) return;
    var suffix = match[2] || "";
    var duration = 900;
    var started = null;
    var finished = false;

    function frame(now) {
      if (started === null) started = now;
      var progress = Math.min(1, (now - started) / duration);
      var eased = 1 - Math.pow(1 - progress, 4);
      el.textContent = Math.round(target * eased) + suffix;
      if (progress < 1) {
        window.requestAnimationFrame(frame);
      } else {
        el.textContent = target + suffix;
        finished = true;
      }
    }

    el.textContent = "0" + suffix;
    function begin() {
      window.requestAnimationFrame(frame);
    }
    if (delay) window.setTimeout(begin, delay);
    else begin();

    // If the tab is hidden, rAF pauses; make sure the final value appears.
    window.setTimeout(function () {
      if (!finished) {
        el.textContent = target + suffix;
        finished = true;
      }
    }, delay + duration + 250);
  }

  function initCounters() {
    var nums = document.querySelectorAll(".stat-num");
    for (var i = 0; i < nums.length; i++) {
      if (nums[i].closest && nums[i].closest("[data-no-count]")) continue;
      countUp(nums[i], 160 + i * 45);
    }
  }

  /* ---- Top bar scroll state ----------------------------------------- */
  function initTopbar() {
    var topbar = document.querySelector(".topbar");
    if (!topbar) return;
    var ticking = false;

    function update() {
      var offset =
        window.pageYOffset ||
        (document.documentElement && document.documentElement.scrollTop) ||
        0;
      topbar.classList.toggle("scrolled", offset > 6);
      ticking = false;
    }

    function request() {
      if (ticking) return;
      ticking = true;
      if (typeof window.requestAnimationFrame === "function") {
        window.requestAnimationFrame(update);
      } else {
        window.setTimeout(update, 80);
      }
    }

    window.addEventListener("scroll", request, { passive: true });
    update();
  }

  /* ---- Modal -------------------------------------------------------- */
  function openModal(backdrop) {
    if (!backdrop) return;
    var modal = backdrop.querySelector(".modal");
    backdrop.hidden = false;
    backdrop.classList.add("open");
    if (!canAnimate()) return;

    animate(
      backdrop,
      [{ opacity: 0 }, { opacity: 1 }],
      { duration: 220, fill: "both" }
    );
    if (modal) {
      animate(
        modal,
        [
          { opacity: 0, transform: "translate3d(0, 10px, 0) scale(0.97)" },
          { opacity: 1, transform: "translate3d(0, 0, 0) scale(1)" }
        ],
        { duration: 340, easing: EASE_SPRING, fill: "both" }
      );
    }
  }

  function closeModal(backdrop, done) {
    if (!backdrop) {
      if (done) done();
      return;
    }
    var modal = backdrop.querySelector(".modal");

    function finish() {
      backdrop.hidden = true;
      backdrop.classList.remove("open");
      if (done) done();
    }

    if (!canAnimate()) {
      finish();
      return;
    }

    var backdropAnim = animate(
      backdrop,
      [{ opacity: 1 }, { opacity: 0 }],
      { duration: 180, easing: EASE_IN, fill: "both", hold: true }
    );
    var modalAnim = modal
      ? animate(
          modal,
          [
            { opacity: 1, transform: "translate3d(0, 0, 0) scale(1)" },
            { opacity: 0, transform: "translate3d(0, 6px, 0) scale(0.98)" }
          ],
          { duration: 160, easing: EASE_IN, fill: "both", hold: true }
        )
      : null;

    var pending = 2;
    function partDone() {
      pending--;
      if (pending > 0) return;
      cancel(backdropAnim);
      cancel(modalAnim);
      finish();
    }
    settle(backdropAnim, partDone);
    settle(modalAnim, partDone);
  }

  /* ---- Toast -------------------------------------------------------- */
  function toastIn(toast) {
    animate(
      toast,
      [
        { opacity: 0, transform: "translate3d(0, 12px, 0) scale(0.96)" },
        { opacity: 1, transform: "translate3d(0, 0, 0) scale(1)" }
      ],
      { duration: 320, easing: EASE_SPRING, fill: "both" }
    );
  }

  function toastOut(toast, done) {
    var animation = animate(
      toast,
      [
        { opacity: 1, transform: "translate3d(0, 0, 0) scale(1)" },
        { opacity: 0, transform: "translate3d(0, 6px, 0) scale(0.98)" }
      ],
      { duration: 180, easing: EASE_IN, fill: "both", hold: true }
    );
    settle(animation, function () {
      cancel(animation);
      if (done) done();
    });
  }

  /* ---- Flash -------------------------------------------------------- */
  function flashOut(flash, done) {
    var animation = animate(
      flash,
      [
        { opacity: 1, transform: "translate3d(0, 0, 0)" },
        { opacity: 0, transform: "translate3d(0, -6px, 0)" }
      ],
      { duration: 180, easing: EASE_IN, fill: "both", hold: true }
    );
    settle(animation, function () {
      cancel(animation);
      if (done) done();
    });
  }

  /* ---- Mobile navigation ------------------------------------------- */
  function navIn(nav) {
    if (!canAnimate() || !nav) return;
    animate(
      nav,
      [
        { opacity: 0, transform: "translate3d(0, -8px, 0)" },
        { opacity: 1, transform: "translate3d(0, 0, 0)" }
      ],
      { duration: 240, fill: "both" }
    );
    var links = nav.querySelectorAll("a");
    var limit = Math.min(links.length, 10);
    for (var i = 0; i < limit; i++) {
      animate(
        links[i],
        [
          { opacity: 0, transform: "translate3d(-6px, 0, 0)" },
          { opacity: 1, transform: "translate3d(0, 0, 0)" }
        ],
        { duration: 220, delay: 24 + i * 20, fill: "both" }
      );
    }
  }

  /* ---- Small feedback ---------------------------------------------- */
  function pop(el) {
    animate(
      el,
      [
        { transform: "scale(1)" },
        { transform: "scale(1.06)" },
        { transform: "scale(1)" }
      ],
      { duration: 260, easing: EASE_SPRING, fill: "none" }
    );
  }

  function init() {
    initTopbar();
    if (!canAnimate()) return;
    initCounters();
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

  window.LPMotion = {
    enabled: canAnimate,
    animate: animate,
    openModal: openModal,
    closeModal: closeModal,
    toastIn: toastIn,
    toastOut: toastOut,
    flashOut: flashOut,
    navIn: navIn,
    pop: pop,
    initCounters: initCounters
  };

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init, { once: true });
  } else {
    init();
  }
})();
