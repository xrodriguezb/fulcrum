// Captures the console screenshots used in the README.
//
// The captures are generated rather than taken by hand so that a stale image is
// a command away from being correct, and so that what a reviewer sees is a
// running system with real data rather than a mock.
//
// It drives Chrome over the DevTools protocol using the WebSocket client that
// ships with Node, which keeps the repository free of a browser automation
// dependency that nothing else would use.

import { spawn } from 'node:child_process';
import { mkdtemp, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const url = process.env.FULCRUM_CONSOLE_URL ?? 'http://localhost:3000/';
const chrome =
  process.env.CHROME_PATH ?? '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const port = Number(process.env.CHROME_PORT ?? 9222);

/** shots lists what to capture: a viewport, an output file, and an optional act. */
const shots = [
  { file: 'docs/images/console.png', width: 1280, height: 900, openDetail: true },
  { file: 'docs/images/console-375px.png', width: 375, height: 900, openDetail: false },
];

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

async function endpoint() {
  for (let attempt = 0; attempt < 50; attempt += 1) {
    try {
      const response = await fetch(`http://127.0.0.1:${port}/json/list`);
      const targets = await response.json();
      // The browser endpoint speaks a different subset of the protocol: the Page
      // domain exists only on a page target.
      const page = targets.find((target) => target.type === 'page');
      if (page !== undefined) {
        return page.webSocketDebuggerUrl;
      }
    } catch {
      // Chrome is not listening yet.
    }
    await sleep(200);
  }
  throw new Error('Chrome never opened a page target on its debugging port');
}

function client(socket) {
  let nextId = 1;
  const pending = new Map();

  socket.addEventListener('message', (event) => {
    const message = JSON.parse(event.data);
    const waiter = pending.get(message.id);
    if (waiter !== undefined) {
      pending.delete(message.id);
      // A protocol error has to stop the capture. Silently resolving it writes
      // an empty file and reports success.
      if (message.error !== undefined) {
        waiter.reject(new Error(`${message.error.message} (${waiter.method})`));
        return;
      }
      waiter.resolve(message.result);
    }
  });

  return (method, params = {}) =>
    new Promise((resolve, reject) => {
      const id = nextId++;
      pending.set(id, { resolve, reject, method });
      socket.send(JSON.stringify({ id, method, params }));
    });
}

const profile = await mkdtemp(join(tmpdir(), 'fulcrum-shot-'));
const browser = spawn(chrome, [
  '--headless=new',
  `--remote-debugging-port=${port}`,
  `--user-data-dir=${profile}`,
  '--hide-scrollbars',
  '--no-first-run',
  'about:blank',
  '--force-color-profile=srgb',
]);
browser.on('error', (cause) => {
  console.error(`cannot start Chrome: ${cause.message}`);
  process.exit(1);
});

try {
  const socket = new WebSocket(await endpoint());
  await new Promise((resolve) => socket.addEventListener('open', resolve, { once: true }));
  const send = client(socket);

  await send('Page.enable');
  await send('Runtime.enable');

  for (const shot of shots) {
    await send('Emulation.setDeviceMetricsOverride', {
      width: shot.width,
      height: shot.height,
      deviceScaleFactor: 2,
      mobile: shot.width < 600,
    });
    await send('Page.navigate', { url });
    // The console fills from three requests and then a stream, and the sparkline
    // needs a second sample, so the wait is for data rather than for load.
    await sleep(6000);

    if (shot.openDetail) {
      await send('Runtime.evaluate', {
        expression: `document.querySelector('#orders-panel-region button.link')?.click()`,
      });
      await sleep(1500);
    }

    // The whole page, not the viewport: the detail opens below the fold, and a
    // capture that cut it off would advertise a console the reader cannot see.
    const metrics = await send('Page.getLayoutMetrics');
    const content = metrics.cssContentSize;
    const { data } = await send('Page.captureScreenshot', {
      format: 'png',
      captureBeyondViewport: true,
      clip: { x: 0, y: 0, width: content.width, height: content.height, scale: 1 },
    });
    await writeFile(shot.file, Buffer.from(data, 'base64'));
    console.log(`wrote ${shot.file}`);
  }

  socket.close();
} finally {
  browser.kill();
}
