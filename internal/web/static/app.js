/* Progressive enhancement for the campus leave pass UI.
 * Loaded with `defer` from /static/app.js. No dependencies, no inline code.
 * Every feature is defensive: without JavaScript the server-rendered
 * pages remain usable. */
(function () {
  "use strict";

  // Marks that the enhancement script is alive; CSS uses this to distinguish
  // the no-JS fallback navigation from the collapsible JavaScript version.
  document.documentElement.classList.add("js");

  function ready(fn) {
    if (document.readyState === "loading") {
      document.addEventListener("DOMContentLoaded", fn, { once: true });
    } else {
      fn();
    }
  }

  function closest(node, selector) {
    if (!node || typeof node.closest !== "function") return null;
    return node.closest(selector);
  }

  /* One lazy layout instance per container. The layout module is optional so
     app.js keeps working if it fails to load. */
  var layoutCache = typeof WeakMap === "function" ? new WeakMap() : null;

  function layoutFor(root, options) {
    if (!root || !layoutCache || !window.LPLayout) return null;
    var layout = layoutCache.get(root);
    if (!layout) {
      layout = window.LPLayout.createLayout(root, options);
      if (layout) layoutCache.set(root, layout);
    }
    return layout;
  }

  var TOAST_LAYOUT = { children: ".toast", duration: 300 };
  var PAGE_LAYOUT = { children: ".flash, .main, .footer", duration: 320 };

  /* ------------------------------------------------------------------
   * Accessible toast (used for disabled-action reasons and copy errors)
   * ---------------------------------------------------------------- */
  var toastRegion = null;
  var toastTimers = typeof WeakMap === "function" ? new WeakMap() : null;

  function ensureToastRegion() {
    if (toastRegion && document.body.contains(toastRegion)) return toastRegion;
    toastRegion = document.createElement("div");
    toastRegion.className = "toast-region";
    toastRegion.setAttribute("role", "status");
    toastRegion.setAttribute("aria-live", "polite");
    toastRegion.setAttribute("aria-atomic", "false");
    document.body.appendChild(toastRegion);
    return toastRegion;
  }

  function cleanupToastRegion(region) {
    if (region && region.childElementCount === 0 && region.parentNode) {
      region.parentNode.removeChild(region);
      if (toastRegion === region) toastRegion = null;
    }
  }

  function removeToast(toast, region) {
    if (!toast || toast.getAttribute("data-dismissed") === "1") return;
    toast.setAttribute("data-dismissed", "1");

    if (toastTimers) {
      var timer = toastTimers.get(toast);
      if (timer) window.clearTimeout(timer);
      toastTimers.delete(toast);
    }

    function finish() {
      var layout = layoutFor(region, TOAST_LAYOUT);
      if (layout) layout.record();
      if (toast.parentNode) toast.parentNode.removeChild(toast);
      if (layout) layout.animate();
      cleanupToastRegion(region);
    }

    if (window.LPMotion) window.LPMotion.toastOut(toast, finish);
    else finish();
  }

  function announce(message, timeout) {
    var text = String(message || "").trim();
    if (!text) return;

    var region = ensureToastRegion();
    var toast = document.createElement("div");
    toast.className = "toast";

    var body = document.createElement("span");
    body.className = "toast-text";
    body.textContent = text;
    toast.appendChild(body);

    var close = document.createElement("button");
    close.type = "button";
    close.className = "toast-close";
    close.setAttribute("aria-label", "关闭提示");
    close.textContent = "×";
    close.addEventListener("click", function (event) {
      event.preventDefault();
      event.stopPropagation();
      removeToast(toast, region);
    });
    toast.appendChild(close);

    var layout = layoutFor(region, TOAST_LAYOUT);
    if (layout) layout.record();
    region.appendChild(toast);
    if (layout) layout.animate();
    if (window.LPMotion) window.LPMotion.toastIn(toast);

    var delay =
      typeof timeout === "number"
        ? timeout
        : Math.max(5200, Math.min(12000, text.length * 180));
    var timer = window.setTimeout(function () {
      removeToast(toast, region);
    }, delay);
    if (toastTimers) toastTimers.set(toast, timer);
  }

  /* ------------------------------------------------------------------
   * Mobile navigation
   * ---------------------------------------------------------------- */
  function setupNavigation() {
    var toggle = document.querySelector("[data-nav-toggle]");
    var nav = document.getElementById("mainnav");
    if (!toggle || !nav) return;

    var narrow = window.matchMedia
      ? window.matchMedia("(max-width: 900px)")
      : null;

    function isMobile() {
      return !narrow || narrow.matches;
    }

    function focusToggle() {
      try {
        toggle.focus();
      } catch (err) {
        /* no-op */
      }
    }

    function isOpen() {
      return nav.classList.contains("open");
    }

    function setOpen(open, returnFocus) {
      var wasOpen = nav.classList.contains("open");
      toggle.setAttribute("aria-expanded", open ? "true" : "false");

      if (open === wasOpen) {
        if (!open && returnFocus) focusToggle();
        return;
      }

      if (open) {
        nav.classList.add("open");
        if (isMobile() && window.LPLayout) window.LPLayout.expand(nav);
        if (window.LPMotion) window.LPMotion.navIn(nav);
        return;
      }

      function finish() {
        nav.classList.remove("open");
        if (returnFocus) focusToggle();
      }

      if (isMobile() && window.LPLayout) {
        window.LPLayout.collapse(nav, finish);
      } else {
        if (window.LPLayout) window.LPLayout.reset(nav);
        finish();
      }
    }

    toggle.addEventListener("click", function () {
      setOpen(!isOpen(), false);
    });

    nav.addEventListener("click", function (event) {
      if (closest(event.target, "a")) setOpen(false, false);
    });

    document.addEventListener("keydown", function (event) {
      if ((event.key === "Escape" || event.key === "Esc") && isOpen()) {
        setOpen(false, true);
      }
    });

    document.addEventListener("click", function (event) {
      if (!isOpen()) return;
      if (nav.contains(event.target) || toggle.contains(event.target)) return;
      setOpen(false, false);
    });

    var desktop = window.matchMedia
      ? window.matchMedia("(min-width: 901px)")
      : null;
    if (desktop) {
      var onChange = function (event) {
        if (event.matches) setOpen(false, false);
      };
      if (typeof desktop.addEventListener === "function") {
        desktop.addEventListener("change", onChange);
      } else if (typeof desktop.addListener === "function") {
        desktop.addListener(onChange);
      }
    }
  }

  /* ------------------------------------------------------------------
   * Flash message dismissal
   * ---------------------------------------------------------------- */
  function setupFlashDismiss() {
    document.addEventListener("click", function (event) {
      var trigger = closest(event.target, "[data-dismiss]");
      if (!trigger) return;
      var flash = closest(trigger, ".flash");
      if (!flash) return;
      event.preventDefault();
      if (flash.getAttribute("data-dismissing") === "1") return;
      flash.setAttribute("data-dismissing", "1");
      function finish() {
        var layout = layoutFor(document.body, PAGE_LAYOUT);
        if (layout) layout.record();
        if (flash.parentNode) flash.parentNode.removeChild(flash);
        if (layout) layout.animate();
      }
      if (window.LPMotion) window.LPMotion.flashOut(flash, finish);
      else finish();
    });
  }

  /* ------------------------------------------------------------------
   * Confirmations for destructive links / forms / buttons
   * ---------------------------------------------------------------- */
  function setupConfirmations() {
    document.addEventListener("submit", function (event) {
      var form = event.target;
      if (!form || form.tagName !== "FORM") return;
      var message = form.getAttribute("data-confirm");
      if (!message) return;
      if (!window.confirm(message)) {
        event.preventDefault();
        event.stopPropagation();
      }
    });

    document.addEventListener("click", function (event) {
      var trigger = closest(event.target, "[data-confirm]");
      if (!trigger) return;
      if (trigger.tagName === "FORM") return; // handled by the submit listener
      // A data-confirm button inside a data-confirm form is handled on submit.
      var ownerForm = closest(trigger, "form[data-confirm]");
      if (ownerForm) return;
      var message = trigger.getAttribute("data-confirm");
      if (message && !window.confirm(message)) {
        event.preventDefault();
        event.stopPropagation();
      }
    });
  }

  /* ------------------------------------------------------------------
   * Disabled actions explain themselves instead of silently failing
   * ---------------------------------------------------------------- */
  function setupDisabledReasons() {
    function reasonFor(element) {
      var reason = element.getAttribute("data-reason");
      return reason && reason.trim()
        ? reason.trim()
        : "该操作当前不可用，请按页面提示处理后重试。";
    }

    document.addEventListener("click", function (event) {
      var target = closest(event.target, '[aria-disabled="true"]');
      if (!target) return;
      if (target.hasAttribute("data-reason")) {
        event.preventDefault();
        announce(reasonFor(target));
      }
    });

    document.addEventListener("keydown", function (event) {
      if (event.key !== "Enter" && event.key !== " " && event.key !== "Spacebar") {
        return;
      }
      var target = closest(event.target, '[aria-disabled="true"]');
      if (!target) return;
      if (target.hasAttribute("data-reason")) {
        event.preventDefault();
        announce(reasonFor(target));
      }
    });
  }

  /* ------------------------------------------------------------------
   * Optional popup modal (shown once per day by the backend)
   * ---------------------------------------------------------------- */
  function setupPopups() {
    var popups = document.querySelectorAll("[data-popup-modal]");
    if (!popups.length) return;

    var focusableSelector =
      'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

    Array.prototype.forEach.call(popups, function (backdrop) {
      var previousFocus = document.activeElement;
      var closeButton = backdrop.querySelector("[data-popup-close]");
      var closing = false;

      function restoreFocus() {
        if (
          previousFocus &&
          previousFocus !== document.body &&
          document.contains(previousFocus) &&
          typeof previousFocus.focus === "function"
        ) {
          previousFocus.focus();
        } else if (closeButton && typeof closeButton.blur === "function") {
          closeButton.blur();
        }
      }

      function close() {
        if (closing) return;
        closing = true;
        function finish() {
          closing = false;
          restoreFocus();
        }
        if (window.LPMotion) {
          window.LPMotion.closeModal(backdrop, finish);
        } else {
          backdrop.classList.remove("open");
          backdrop.hidden = true;
          finish();
        }
      }

      function open() {
        closing = false;
        if (window.LPMotion) {
          window.LPMotion.openModal(backdrop);
        } else {
          backdrop.hidden = false;
          backdrop.classList.add("open");
        }
        var initial = closeButton || backdrop.querySelector(focusableSelector);
        if (initial && typeof initial.focus === "function") {
          window.setTimeout(function () {
            initial.focus();
          }, 0);
        }
      }

      backdrop.addEventListener("click", function (event) {
        if (event.target === backdrop) close();
        if (closest(event.target, "[data-popup-close]")) {
          event.preventDefault();
          close();
        }
      });

      document.addEventListener("keydown", function (event) {
        if (backdrop.hidden) return;
        if (event.key === "Escape" || event.key === "Esc") {
          event.preventDefault();
          close();
          return;
        }
        if (event.key !== "Tab") return;

        var focusables = Array.prototype.filter.call(
          backdrop.querySelectorAll(focusableSelector),
          function (element) {
            return element.offsetParent !== null;
          }
        );
        if (!focusables.length) {
          event.preventDefault();
          return;
        }
        var first = focusables[0];
        var last = focusables[focusables.length - 1];
        if (event.shiftKey && document.activeElement === first) {
          event.preventDefault();
          last.focus();
        } else if (!event.shiftKey && document.activeElement === last) {
          event.preventDefault();
          first.focus();
        }
      });

      open();
    });
  }

  /* ------------------------------------------------------------------
   * Copy buttons inside .copybox
   * ---------------------------------------------------------------- */
  function setupCopyButtons() {
    var resetTimers = new WeakMap();

    function markCopied(button) {
      var oldText = button.getAttribute("data-copy-label");
      if (oldText === null) {
        oldText = button.textContent;
        button.setAttribute("data-copy-label", oldText);
      }
      button.textContent = "已复制";
      if (window.LPMotion) window.LPMotion.pop(button);
      var existing = resetTimers.get(button);
      if (existing) window.clearTimeout(existing);
      resetTimers.set(
        button,
        window.setTimeout(function () {
          button.textContent = oldText;
          resetTimers.delete(button);
        }, 2200)
      );
    }

    function selectInput(input) {
      try {
        input.focus();
        input.select();
        if (typeof input.setSelectionRange === "function") {
          input.setSelectionRange(0, input.value.length);
        }
      } catch (err) {
        /* no-op */
      }
    }

    function legacyCopy(input) {
      var ok = false;
      try {
        selectInput(input);
        ok = document.execCommand && document.execCommand("copy");
      } catch (err) {
        ok = false;
      }
      return !!ok;
    }

    document.addEventListener("click", function (event) {
      var button = closest(event.target, "[data-copy]");
      if (!button) return;
      var box = closest(button, ".copybox");
      var input = box ? box.querySelector("[data-copy-input]") : null;
      if (!input) {
        announce("没有可复制的内容。");
        return;
      }
      var value = input.value || input.textContent || "";
      if (!value) {
        announce("没有可复制的内容。");
        return;
      }

      function succeed() {
        markCopied(button);
        announce("已复制到剪贴板。", 2600);
      }

      function fallback() {
        if (legacyCopy(input)) {
          succeed();
          return;
        }
        selectInput(input);
        announce("自动复制失败，已选中文本，请按 Ctrl+C 手动复制。");
      }

      if (
        navigator.clipboard &&
        typeof navigator.clipboard.writeText === "function" &&
        (window.isSecureContext === undefined || window.isSecureContext)
      ) {
        navigator.clipboard.writeText(value).then(succeed, fallback);
      } else {
        fallback();
      }
    });
  }

  /* ------------------------------------------------------------------
   * Print / back buttons
   * ---------------------------------------------------------------- */
  function setupUtilityButtons() {
    document.addEventListener("click", function (event) {
      var printButton = closest(event.target, "[data-print]");
      if (printButton) {
        event.preventDefault();
        window.print();
        return;
      }
      var backButton = closest(event.target, "[data-back]");
      if (backButton) {
        event.preventDefault();
        window.history.back();
      }
    });
  }

  /* ------------------------------------------------------------------
   * Student / staff field switcher used by the recovery forms.
   * Both variants stay visible without JavaScript.
   * ---------------------------------------------------------------- */
  function setupKindForms() {
    var forms = document.querySelectorAll("[data-kind-form]");
    Array.prototype.forEach.call(forms, function (form) {
      var radios = form.querySelectorAll('input[name="kind"][type="radio"]');
      var fields = form.querySelectorAll("[data-kind]");
      if (!radios.length || !fields.length) return;

      function update(animate) {
        var checked = null;
        Array.prototype.forEach.call(radios, function (radio) {
          if (radio.checked) checked = radio;
        });
        if (!checked) return;
        var kind = checked.value;

        function apply() {
          Array.prototype.forEach.call(fields, function (field) {
            field.hidden = field.getAttribute("data-kind") !== kind;
          });
        }

        if (animate && window.LPLayout) {
          window.LPLayout.morph(form, apply, {
            duration: 240,
            enter: "[data-kind]"
          });
        } else {
          apply();
        }
      }

      Array.prototype.forEach.call(radios, function (radio) {
        radio.addEventListener("change", function () {
          update(true);
        });
      });
      update(false);
    });
  }

  ready(function () {
    setupNavigation();
    setupFlashDismiss();
    setupConfirmations();
    setupDisabledReasons();
    setupPopups();
    setupCopyButtons();
    setupUtilityButtons();
    setupKindForms();
  });
})();
