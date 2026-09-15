(() => {
  const root = document.documentElement;
  try { const theme = localStorage.getItem('pipeek-theme'); if (theme === 'dark' || theme === 'light') root.dataset.theme = theme; } catch (_) {}
  const button = document.getElementById('theme');
  const dark = () => root.dataset.theme ? root.dataset.theme === 'dark' : matchMedia('(prefers-color-scheme: dark)').matches;
  const label = () => button.setAttribute('aria-label', `Switch to ${dark() ? 'light' : 'dark'} theme`);
  label();
  button.addEventListener('click', () => { root.dataset.theme = dark() ? 'light' : 'dark'; label(); try { localStorage.setItem('pipeek-theme', root.dataset.theme); } catch (_) {} });
  const metrics = document.getElementById('metrics');
  const status = document.getElementById('connection');
  const interval = Number(document.body.dataset.interval);
  document.getElementById('interval-label').textContent = `${interval / 1000}s`;
  let failed = false;
  let lastSample = metrics.querySelector('[data-sampled]')?.dataset.sampled;
  // Age is computed by the server. Browser elapsed time uses its monotonic clock.
  let ageAtReceipt = Number(document.body.dataset.sampleAge) || 0;
  let receivedAt = performance.now();
  const acceptSample = event => {
    if (event.detail.elt !== metrics) return;
    const sample = metrics.querySelector('[data-sampled]')?.dataset.sampled;
    const age = Number(event.detail.xhr.getResponseHeader('X-PiPeek-Sample-Age'));
    if (sample && sample !== lastSample) {
      lastSample = sample;
      ageAtReceipt = Number.isFinite(age) ? Math.max(0, age) : 0;
      receivedAt = performance.now();
    }
    updateStatus();
  };
  const updateStatus = () => {
    const stale = !lastSample || ageAtReceipt + performance.now() - receivedAt > interval * 3;
    const paused = document.hidden;
    status.textContent = paused ? 'Paused' : failed ? 'Disconnected · retrying' : stale ? 'Stale data · retrying' : 'Live';
    status.classList.toggle('offline', paused || failed || stale);
  };
  const refresh = () => { updateStatus(); if (!document.hidden) htmx.trigger(metrics, 'refresh'); };
  document.body.addEventListener('htmx:beforeRequest', event => { if (document.hidden) event.preventDefault(); });
  document.body.addEventListener('htmx:afterRequest', event => { if (event.detail.elt === metrics) { failed = !event.detail.successful; updateStatus(); } });
  document.body.addEventListener('htmx:afterSwap', acceptSample);
  document.addEventListener('visibilitychange', () => { if (document.hidden) htmx.trigger(metrics, 'htmx:abort'); refresh(); });
  updateStatus();
  setInterval(refresh, interval);
})();
