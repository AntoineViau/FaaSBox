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

  /* --- Outbound clicks ---------------------------------------------- */

  /* The one action worth counting on this page: leaving for the repository.
     Recorded as a GoatCounter event, which does nothing when the script is
     blocked or absent. Docs links are told apart from the repo itself. */
  document.querySelectorAll('a[href^="https://github.com/"]').forEach(function (link) {
    link.addEventListener('click', function () {
      var target = link.href.includes('/blob/') ? 'github-docs' : 'github-repo';
      if (window.goatcounter && window.goatcounter.count) {
        window.goatcounter.count({ path: target, title: 'Outbound: ' + target, event: true });
      }
    });
  });

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
