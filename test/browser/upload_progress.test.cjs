const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { webcrypto } = require('node:crypto');
const { test } = require('node:test');

const html = fs.readFileSync(path.join(__dirname, '../../web/templates/index.html'), 'utf8');
const script = fs.readFileSync(path.join(__dirname, '../../web/static/upload.js'), 'utf8');

function page(size = 10000, options = {}) {
  let now = 0;
  let nextTimer = 0;
  const timers = new Map();
  const elements = new Map();
  const requests = [];
  const uploads = [];
  const refreshes = [];
  for (const match of html.matchAll(/id="([^"]+)"/g)) {
    const events = new Map();
    let value = '';
    const element = {
      textContent: '', disabled: false, files: [], style: {}, attributes: {},
      classList: {add() {}, remove() {}},
      setAttribute(name, value) { this.attributes[name] = value; },
      addEventListener(name, callback) { events.set(name, callback); },
      dispatch(name, event) { events.get(name)(event); }
    };
    Object.defineProperty(element, 'value', {get() { return value; }, set(next) { value = next; if (next === '') this.files = []; }});
    elements.set(match[1], element);
  }
  elements.get('uploadPanel').dataset = {remainingBytes: '10000000', csrfToken: 'a'.repeat(43), directory: 'My folder/docs'};
  elements.get('directory').webkitdirectory = true;
  class XHR {
    constructor() { this.upload = {}; this.headers = {}; uploads.push(this); }
    open(method, url) { this.method = method; this.url = url; }
    setRequestHeader(name, value) { this.headers[name] = value; }
    getResponseHeader() { return null; }
    send(body) { this.body = body; }
  }
  vm.runInNewContext(script, {
    document: {getElementById(id) { return elements.get(id); }},
    XMLHttpRequest: XHR, FormData, crypto: webcrypto, AbortController,
    performance: {now() { return now; }},
    setTimeout(callback, delay) {
      const id = ++nextTimer;
      timers.set(id, {callback, due: now + delay});
      return id;
    },
    clearTimeout(id) { timers.delete(id); },
    fetch(url, fetchOptions) {
      if (url.startsWith('/files?')) {
        refreshes.push({url, options: fetchOptions});
        return Promise.resolve({ok: !options.refreshFails, async text() { return '<table><tr><td>test.bin</td></tr></table>'; }});
      }
      return new Promise((resolve, reject) => { requests.push({url, options: fetchOptions, resolve, reject}); });
    }
  });
  function text(id) { return elements.get(id).textContent; }
  function select(bytes) {
    elements.get('file').files = [new File([new Uint8Array(bytes)], 'test.bin')];
    elements.get('file').dispatch('change');
  }
  select(size);
  elements.get('btn').dispatch('click');
  const xhr = uploads[0];
  async function settle() { for (let i = 0; i < 8; i++) await Promise.resolve(); }
  async function reply(state, bytes, at, status = 200) {
    now = at;
    const request = requests.find(request => !request.settled);
    assert.ok(request, 'expected a pending progress request');
    request.settled = true;
    request.resolve({ok: status === 200, status, async json() { return {state, bytes_received: bytes}; }});
    await settle();
  }
  async function poll() {
    const timer = [...timers.entries()].find(([, timer]) => timer.due - now === 750);
    assert.ok(timer, 'expected another progress poll');
    timers.delete(timer[0]);
    now = timer[1].due;
    timer[1].callback();
    await settle();
  }
  function finish(status = 200) {
    xhr.status = status;
    xhr.responseText = status === 200 ? 'ok\nsaved_as=test.bin\nbytes=' + size : 'disk quota exceeded';
    xhr.onload();
  }
  return {text, elements, xhr, requests, timers, reply, poll, finish, settle, select, refreshes};
}

test('browser send completion does not advance server progress to 100%', async () => {
  const p = page();
  assert.equal(p.xhr.url, '/upload?dir=My%20folder%2Fdocs');
  assert.match(p.xhr.headers['X-Upload-ID'], /^[a-f0-9]{32}$/);
  assert.equal(p.xhr.headers['X-CSRF-Token'], 'a'.repeat(43));
  assert.match(p.requests[0].url, /^\/upload-progress\?id=[a-f0-9]{32}$/);
  assert.equal(p.requests[0].options.credentials, 'same-origin');
  p.xhr.upload.onprogress?.({lengthComputable: true, loaded: 10200, total: 10200});
  p.xhr.upload.onload();
  assert.equal(p.text('pct'), '0%');
  await p.reply('uploading', 2500, 2000);
  assert.equal(p.text('pct'), '25%');
  assert.equal(p.text('up'), '2.44 KB');
  assert.equal(p.text('tot'), '9.77 KB');
  assert.equal(p.text('eta'), '≈ 6s');
  await p.poll();
  await p.reply('uploading', 5000, 4000);
  assert.equal(p.text('pct'), '50%');
  assert.equal(p.text('eta'), '≈ 4s');
  await p.poll();
  await p.reply('complete', 10000, 8000);
  assert.equal(p.text('pct'), '99%');
  assert.equal(p.text('phase'), 'Finishing upload');
  assert.equal(p.text('eta'), 'Finishing…');
  p.finish();
  assert.equal(p.text('pct'), '100%');
  assert.equal(p.elements.get('progress').attributes['aria-valuenow'], '100');
  assert.equal(p.text('phase'), 'Done');
  assert.equal(p.text('eta'), '0s');
  await p.settle();
  assert.match(p.elements.get('fileList').innerHTML, /test.bin/);
  assert.equal(p.refreshes[0].url, '/files?dir=My%20folder%2Fdocs');
  assert.equal(p.timers.size, 0);
});

test('a failed explorer refresh keeps a successful upload complete and asks for a reload', async () => {
  const p = page(10000, {refreshFails: true});
  p.elements.get('fileList').innerHTML = '<table>existing files</table>';
  p.finish();
  await p.settle();
  assert.equal(p.text('phase'), 'Done');
  assert.equal(p.text('pct'), '100%');
  assert.equal(p.elements.get('fileList').innerHTML, '<table>existing files</table>');
  assert.match(p.text('explorerMessage'), /Reload the page/);
});

test('proxy buffering, queueing, and storage checks do not invent progress or an ETA', async () => {
  const p = page();
  p.xhr.upload.onload();
  await p.reply('', 0, 1000, 404);
  assert.equal(p.text('pct'), '0%');
  assert.equal(p.text('phase'), 'Connecting to server');
  assert.equal(p.text('eta'), 'Calculating…');
  await p.poll();
  await p.reply('queued', 0, 2000);
  assert.equal(p.text('phase'), 'Waiting for another upload');
  assert.equal(p.text('eta'), 'Waiting');
  await p.poll();
  await p.reply('preparing', 0, 3000);
  assert.equal(p.text('phase'), 'Checking storage');
  assert.equal(p.text('eta'), 'Calculating…');
  assert.equal(p.text('pct'), '0%');
  p.finish();
});

test('a stalled upload stops reporting a countdown and recovers when data arrives', async () => {
  const p = page();
  await p.reply('uploading', 5000, 2000);
  assert.equal(p.text('eta'), '≈ 2s');
  await p.poll();
  await p.reply('uploading', 5000, 4000);
  await p.poll();
  await p.reply('uploading', 5000, 8000);
  assert.equal(p.text('pct'), '50%');
  assert.equal(p.text('spd'), '0 B/s');
  assert.equal(p.text('eta'), 'Waiting for data');
  await p.poll();
  await p.reply('uploading', 7500, 10000);
  assert.equal(p.text('pct'), '75%');
  assert.match(p.text('eta'), /^≈ \d+s$/);
  p.finish();
});

test('progress polling failure allows the upload to finish and late responses cannot reset it', async () => {
  const p = page();
  p.requests[0].settled = true;
  p.requests[0].reject(new Error('progress connection lost'));
  await p.settle();
  assert.equal(p.text('eta'), 'Unavailable');
  assert.equal(p.text('pct'), '0%');
  await p.poll();
  const pending = p.requests[1];
  p.finish();
  assert.equal(pending.options.signal.aborted, true);
  await p.reply('uploading', 1000, 5000);
  assert.equal(p.text('pct'), '100%');
  assert.equal(p.text('eta'), '0s');
  assert.equal(p.text('phase'), 'Done');
  assert.equal(p.timers.size, 0);
});

test('a rejected upload never shows 100% even after the server receives all bytes', async () => {
  const p = page();
  await p.reply('finalizing', 10000, 2000);
  assert.equal(p.text('pct'), '99%');
  p.finish(413);
  assert.equal(p.text('pct'), '99%');
  assert.equal(p.text('phase'), 'Upload failed: disk quota exceeded');
  assert.equal(p.text('eta'), '—');
  assert.equal(p.elements.get('btn').disabled, false);
  assert.equal(p.timers.size, 0);
});

for (const [event, phase] of [['onabort', 'Canceled'], ['onerror', 'Network error. Please try again.']]) {
  test(`${event} stops polling and clears the estimate`, async () => {
    const p = page();
    const pending = p.requests[0];
    p.xhr[event]();
    assert.equal(p.text('phase'), phase);
    assert.equal(p.text('eta'), '—');
    assert.equal(p.text('pct'), '0%');
    assert.equal(pending.options.signal.aborted, true);
    await p.reply('uploading', 5000, 4000);
    assert.equal(p.text('phase'), phase);
    assert.equal(p.timers.size, 0);
    assert.equal(p.elements.get('file').disabled, false);
  });
}

test('zero-byte files wait for confirmation and estimates support hours', async () => {
  const empty = page(0);
  await empty.reply('uploading', 0, 2000);
  assert.equal(empty.text('pct'), '0%');
  assert.equal(empty.text('eta'), 'Calculating…');
  empty.finish();
  assert.equal(empty.text('pct'), '100%');
  assert.equal(empty.text('eta'), '0s');
  const slow = page();
  await slow.reply('uploading', 1, 1000);
  assert.equal(slow.text('eta'), '≈ 2h 46m');
  slow.finish();
  slow.select(100);
  assert.equal(slow.text('pct'), '0%');
  assert.equal(slow.text('eta'), '—');
});
