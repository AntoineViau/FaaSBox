/* FaaSBox landing page — theme toggle and code copy. No dependencies.
   User-facing strings come from the page's data attributes so this file
   stays language-agnostic. */

(function () {
  'use strict';

  var root = document.documentElement;
  var strings = document.body.dataset;

  /* --- Theme -------------------------------------------------------- */

  var toggle = document.querySelector('[data-theme-toggle]');

  function label() {
    var dark = root.getAttribute('data-theme') === 'dark';
    toggle.setAttribute('aria-label', dark ? strings.toLight : strings.toDark);
  }

  if (toggle) {
    label();
    toggle.addEventListener('click', function () {
      var next = root.getAttribute('data-theme') === 'dark' ? 'light' : 'dark';
      root.setAttribute('data-theme', next);
      label();
      try { localStorage.setItem('faasbox-theme', next); } catch (e) { /* private mode */ }
    });
  }

  /* Follow the system only while the visitor has not chosen one. */
  var media = window.matchMedia('(prefers-color-scheme: dark)');
  media.addEventListener('change', function (event) {
    var chosen = null;
    try { chosen = localStorage.getItem('faasbox-theme'); } catch (e) { /* private mode */ }
    if (chosen) return;
    root.setAttribute('data-theme', event.matches ? 'dark' : 'light');
    if (toggle) label();
  });

  /* --- Language ----------------------------------------------------- */

  /* Each option holds its own URL, so adding a language needs no change here. */
  var langSelect = document.querySelector('[data-lang-select]');
  if (langSelect) {
    langSelect.addEventListener('change', function () {
      window.location.href = langSelect.value;
    });
  }

  /* --- Copy --------------------------------------------------------- */

  document.querySelectorAll('[data-copy]').forEach(function (button) {
    var block = button.closest('.code');
    var source = block && block.querySelector('code');
    if (!source) return;

    function restore() {
      button.textContent = strings.copyLabel;
      button.removeAttribute('data-done');
    }

    button.addEventListener('click', function () {
      navigator.clipboard.writeText(source.innerText).then(function () {
        button.textContent = strings.copiedLabel;
        button.setAttribute('data-done', '');
        setTimeout(restore, 1600);
      }).catch(function () {
        button.textContent = strings.copyFailedLabel;
        setTimeout(restore, 1600);
      });
    });
  });
})();
