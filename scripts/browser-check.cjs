// Development-only check: requires Playwright and a running PiPeek instance.
// NODE_PATH=/path/to/node_modules node scripts/browser-check.cjs http://127.0.0.1:18080/
const { chromium } = require('playwright');
const assert = require('node:assert/strict');
(async () => {
  const browser = await chromium.launch();
  const base = process.argv[2] || 'http://127.0.0.1:18080/';
  try {
    for (const offset of [-60000, 60000]) {
      const page = await browser.newPage();
      await page.addInitScript(offset => { const now = Date.now; Date.now = () => now() + offset; }, offset);
      await page.goto(base);
      await page.waitForFunction(() => document.getElementById('connection').textContent === 'Live');
      assert.equal(await page.evaluate(() => htmx.config.timeout), 10000);
      await page.close();
    }
    const page = await browser.newPage();
    let count = 0;
    await page.route('**/metrics', route => { count++; if (count > 1) return route.continue(); });
    await page.goto(base);
    await page.evaluate(() => htmx.trigger(document.getElementById('metrics'), 'refresh'));
    await page.waitForFunction(() => document.getElementById('connection').textContent.includes('Disconnected'), { }, { timeout: 15000 });
    await page.waitForFunction(() => document.getElementById('connection').textContent === 'Live', { }, { timeout: 15000 });
    assert(count >= 2, 'stalled request prevented retries');
    await page.unroute('**/metrics');
    const response = await page.request.get(new URL('metrics', base).href);
    const frozen = await response.text();
    await page.route('**/metrics', route => route.fulfill({ status: 200, contentType: 'text/html', headers: { 'X-PiPeek-Sample-Age': '0' }, body: frozen }));
    const interval = await page.evaluate(() => Number(document.body.dataset.interval));
    await page.waitForFunction(() => document.getElementById('connection').textContent.includes('Stale data'), { }, { timeout: interval * 5 + 2000 });
    await page.unroute('**/metrics');
    await page.waitForFunction(() => document.getElementById('connection').textContent === 'Live');
    console.log('PASS: ±60s clock skew, finite request timeout, stalled-request recovery, frozen-sample detection and recovery');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exit(1); });
