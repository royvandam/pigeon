const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { webcrypto } = require('node:crypto');
const { test } = require('node:test');

const html = ['index.html', 'uploads.html'].map(name => fs.readFileSync(path.join(__dirname, '../../web/templates', name), 'utf8')).join('');
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
      textContent: '', disabled: false, files: [], style: {}, attributes: {}, clicks: 0,
      click() { this.clicks++; },
      classList: {add() {}, remove() {}},
      setAttribute(name, value) { this.attributes[name] = value; },
      addEventListener(name, callback) { events.set(name, callback); },
      dispatch(name, event) { events.get(name)(event); }
    };
    Object.defineProperty(element, 'value', {get() { return value; }, set(next) { value = next; if (next === '') this.files = []; }});
    elements.set(match[1], element);
  }
  elements.get('uploadPanel').dataset = {remainingBytes: '10000000', csrfToken: 'a'.repeat(43), directory: 'My folder/docs', ...options.settings};
  elements.get('directory').webkitdirectory = true;
  // The page behind an upload link has no file list or storage display.
  for (const id of options.missing || []) elements.delete(id);
  class XHR {
    constructor() { this.upload = {}; this.headers = {}; uploads.push(this); }
    open(method, url) { this.method = method; this.url = url; }
    setRequestHeader(name, value) { this.headers[name] = value; }
    getResponseHeader() { return null; }
    send(body) { this.body = body; }
    abort() { this.aborted = true; this.onabort(); }
  }
  const documentEvents = new Map();
  vm.runInNewContext(script, {
    document: {
      getElementById(id) { return elements.get(id); },
      addEventListener(name, callback) { documentEvents.set(name, callback); }
    },
    XMLHttpRequest: XHR, FormData, URLSearchParams, crypto: webcrypto, AbortController,
    performance: {now() { return now; }},
    setTimeout(callback, delay) {
      const id = ++nextTimer;
      timers.set(id, {callback, delay, due: now + delay});
      return id;
    },
    clearTimeout(id) { timers.delete(id); },
    fetch(url, fetchOptions) {
      if (url.startsWith('/files?')) {
        refreshes.push({url, options: fetchOptions});
        const status = options.refreshStatus || (options.refreshFails ? 500 : 200);
        return Promise.resolve({ok: status === 200, status, async text() { return '<table><tr><td>test.bin</td></tr></table>'; }});
      }
      return new Promise((resolve, reject) => { requests.push({url, options: fetchOptions, resolve, reject}); });
    }
  });
  function text(id) { return elements.get(id).textContent; }
  function select(bytes) {
    elements.get('file').files = [new File([new Uint8Array(bytes)], 'test.bin')];
    elements.get('file').dispatch('change');
  }
  // An idle page waits for a test to select or drop files.
  if (!options.idle) {
    select(size);
    elements.get('btn').dispatch('click');
  }
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
  function finish(status = 200, responseText = 'disk quota exceeded') {
    xhr.status = status;
    xhr.responseText = status === 200 ? 'ok\nsaved_as=test.bin\nbytes=' + size : responseText;
    xhr.onload();
  }
  // closeProgress runs the timer that closes the progress panel after a
  // successful upload, if one is pending, and reports whether it ran.
  function closeProgress() {
    const timer = [...timers.entries()].find(([, timer]) => timer.delay === 3000);
    if (!timer) return false;
    timers.delete(timer[0]);
    now = Math.max(now, timer[1].due);
    timer[1].callback();
    return true;
  }
  // drag sends a document drag event carrying dataTransfer, and returns it.
  function drag(name, dataTransfer) {
    const event = {dataTransfer, prevented: false, preventDefault() { this.prevented = true; }};
    documentEvents.get(name)(event);
    return event;
  }
  return {text, elements, xhr, uploads, requests, timers, reply, poll, finish, settle, select, refreshes, drag, closeProgress};
}

test('browser send completion does not advance server progress to 100%', async () => {
  const p = page();
  assert.equal(p.xhr.url, '/upload?dir=My%20folder%2Fdocs');
  assert.match(p.xhr.headers['X-Upload-ID'], /^[a-f0-9]{32}$/);
  assert.equal(p.xhr.headers['X-CSRF-Token'], 'a'.repeat(43));
  assert.match(p.requests[0].url, /^\/upload-progress\?id=[a-f0-9]{32}$/);
  assert.equal(p.requests[0].options.credentials, 'same-origin');
  assert.equal(p.xhr.headers['X-Requested-With'], 'XMLHttpRequest');
  assert.equal(p.requests[0].options.headers['X-Requested-With'], 'XMLHttpRequest');
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
  assert.equal(p.refreshes[0].options.headers['X-Requested-With'], 'XMLHttpRequest');
  assert.deepEqual([...p.timers.values()].map(timer => timer.delay), [3000], 'only the timer that closes the progress panel');
});

test('expired sessions ask for a reload instead of showing the server response', async () => {
  const rejected = page();
  rejected.finish(401, 'unauthorized');
  assert.equal(rejected.text('phase'), 'Upload failed: Your session has expired. Reload the page to sign in again.');

  const refreshed = page(10000, {refreshStatus: 401});
  refreshed.finish();
  await refreshed.settle();
  assert.equal(refreshed.text('phase'), 'Done');
  assert.equal(refreshed.text('explorerMessage'), 'Your session has expired. Reload the page to sign in again.');
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
  assert.equal(p.closeProgress(), true, 'only the progress panel waits to close');
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

for (const [event, phase] of [['onabort', 'Canceling…'], ['onerror', 'Network error. Please try again.']]) {
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

test('the storage check rounds files up to the server allocation unit', () => {
  const settings = {remainingBytes: '8192', allocationUnit: '4096'};
  const fits = page(4097, {settings});
  assert.ok(fits.xhr, 'a file needing two units fits in two units');

  const tooBig = page(8193, {settings});
  assert.equal(tooBig.xhr, undefined);
  assert.equal(tooBig.elements.get('btn').disabled, true);
  assert.equal(tooBig.text('phase'), 'Not enough storage: 12.00 KB needed, 8.00 KB available.');
  assert.equal(tooBig.text('fileSize'), '8.00 KB', 'the selection still shows its real size');
});

test('the upload link page posts to its link without a session or a storage check', async () => {
  const p = page(10000, {settings: {remainingBytes: undefined, csrfToken: undefined, directory: undefined, uploadUrl: '/u/link-token', progressUrl: '/u/link-token/progress'}, missing: ['fileList', 'explorerMessage', 'storageUsed', 'storageQuota', 'storageRemaining']});
  assert.equal(p.xhr.method, 'POST');
  assert.equal(p.xhr.url, '/u/link-token');
  assert.equal(p.xhr.headers['X-CSRF-Token'], undefined);
  assert.ok(p.requests[0].url.startsWith('/u/link-token/progress?id='));
  await p.reply('complete', 10000, 1000);
  p.finish(200);
  await p.settle();
  assert.equal(p.text('phase'), 'Done');
  assert.equal(p.refreshes.length, 0);
});

// Dropped items as browsers describe them: entries for files and folders,
// whose folders are read a batch at a time.
function fileEntry(name, contents = 'x') {
  return {name, isFile: true, isDirectory: false, file(resolve) { resolve(new File([contents], name)); }};
}
function folderEntry(name, children) {
  return {name, isFile: false, isDirectory: true, createReader() {
    let next = 0;
    return {readEntries(resolve) { resolve(children.slice(next, next += 2)); }};
  }};
}
function dropped(entries) {
  return {types: ['Files'], items: entries.map(entry => ({kind: 'file', webkitGetAsEntry() { return entry; }})), files: [], dropEffect: 'none'};
}
async function settled() { for (let i = 0; i < 50; i++) await Promise.resolve(); }

test('dropped files and folders upload at once with their folder structure', async () => {
  const p = page(0, {idle: true});
  const transfer = dropped([
    folderEntry('Photos', [fileEntry('a.jpg'), folderEntry('trip', [fileEntry('b.jpg')]), fileEntry('c.jpg'), folderEntry('empty', [])]),
    fileEntry('notes.txt', 'hello')
  ]);
  assert.equal(p.drag('dragenter', transfer).prevented, true);
  assert.equal(p.elements.get('dropZone').hidden, false);
  assert.equal(p.drag('dragover', transfer).prevented, true);
  assert.equal(transfer.dropEffect, 'copy');
  assert.equal(p.drag('drop', transfer).prevented, true);
  assert.equal(p.elements.get('dropZone').hidden, true);
  await settled();
  assert.equal(p.uploads.length, 1);
  assert.equal(p.uploads[0].url, '/upload?dir=My%20folder%2Fdocs');
  assert.deepEqual(p.uploads[0].body.getAll('file').map(file => file.name), ['Photos/a.jpg', 'Photos/trip/b.jpg', 'Photos/c.jpg', 'notes.txt']);
  assert.equal(p.text('fileName'), 'Photos and 1 more');
  assert.equal(p.text('fileCount'), '4 files');
  assert.equal(p.text('phase'), 'Connecting to server');
});

test('a drop during an upload is refused and the overlay follows nested drags', async () => {
  const p = page();
  const transfer = dropped([fileEntry('late.txt')]);
  p.drag('dragenter', transfer);
  p.drag('dragenter', transfer); // Entering a child element.
  p.drag('dragleave', transfer);
  assert.equal(p.elements.get('dropZone').hidden, false);
  p.drag('dragleave', transfer);
  assert.equal(p.elements.get('dropZone').hidden, true);
  p.drag('dragover', transfer);
  assert.equal(transfer.dropEffect, 'none');
  p.drag('drop', transfer);
  await settled();
  assert.equal(p.uploads.length, 1);
  assert.match(p.text('phase'), /Wait for the current upload to finish/);
});

test('dragging text is left alone, and empty or oversized drops do not upload', async () => {
  const p = page(0, {idle: true, settings: {remainingBytes: '8192', allocationUnit: '4096'}});
  const text = {types: ['text/plain'], items: [], files: []};
  assert.equal(p.drag('dragenter', text).prevented, false);
  assert.equal(p.drag('drop', text).prevented, false);
  assert.notEqual(p.elements.get('dropZone').hidden, false, 'the overlay never showed');

  p.drag('drop', dropped([folderEntry('empty', [])]));
  await settled();
  assert.equal(p.uploads.length, 0);
  assert.equal(p.text('phase'), 'The dropped folders contain no files.');

  p.drag('drop', dropped([fileEntry('big.bin', 'x'.repeat(9000))]));
  await settled();
  assert.equal(p.uploads.length, 0);
  assert.match(p.text('phase'), /^Not enough storage/);
  assert.equal(p.elements.get('btn').disabled, true);
});

test('browsers without folder entries upload the dropped files', async () => {
  const p = page(0, {idle: true});
  const transfer = {types: ['Files'], items: [{kind: 'file'}], files: [new File(['x'], 'plain.txt')]};
  p.drag('drop', transfer);
  await settled();
  assert.deepEqual(p.uploads[0].body.getAll('file').map(file => file.name), ['plain.txt']);
});

test('one area chooses files or a folder, and progress shows only once an upload starts', () => {
  const p = page(0, {idle: true});
  p.elements.get('chooseFiles').dispatch('click');
  p.elements.get('chooseFolder').dispatch('click');
  assert.equal(p.elements.get('file').clicks, 1);
  assert.equal(p.elements.get('directory').clicks, 1);
  p.select(1000);
  assert.equal(p.elements.get('progressPanel').hidden, true);
  p.elements.get('btn').dispatch('click');
  assert.equal(p.elements.get('progressPanel').hidden, false);
  assert.equal(p.elements.get('chooseFiles').disabled, true);
  assert.equal(p.elements.get('chooseFolder').disabled, true);
  // A finished upload's result shows for a few seconds, then closes.
  p.uploads[0].status = 200;
  p.uploads[0].responseText = 'ok';
  p.uploads[0].onload();
  assert.equal(p.elements.get('progressPanel').hidden, false);
  assert.equal(p.elements.get('chooseFiles').disabled, false);
  p.closeProgress();
  assert.equal(p.elements.get('progressPanel').hidden, true);
});

test('a failed upload keeps its progress until the next selection', () => {
  const p = page();
  p.finish(413);
  assert.equal(p.elements.get('progressPanel').hidden, false);
  assert.equal(p.closeProgress(), false, 'nothing closes it on its own');
  p.select(10);
  assert.equal(p.elements.get('progressPanel').hidden, true);
});

test('a new selection or upload is not hidden by an earlier upload\'s timer', () => {
  const p = page();
  p.finish(200);
  p.select(10);
  p.elements.get('btn').dispatch('click');
  assert.equal(p.elements.get('progressPanel').hidden, false);
  assert.equal(p.closeProgress(), false, 'the earlier timer was canceled');
  assert.equal(p.elements.get('progressPanel').hidden, false);
});

// answer resolves a pending request with status and a JSON body.
async function answer(request, status, body = {}) {
  request.settled = true;
  request.resolve({ok: status >= 200 && status < 300, status, async json() { return body; }, async text() { return ''; }});
  for (let i = 0; i < 20; i++) await Promise.resolve();
}

function cancelRequest(p) {
  return p.requests.find(request => request.options?.method === 'POST' && !request.settled);
}

test('Cancel shows only while an upload runs, and reports what the server did', async () => {
  const p = page();
  const cancel = p.elements.get('cancelUpload');
  assert.equal(cancel.hidden, false);
  cancel.dispatch('click');
  assert.equal(p.xhr.aborted, true);
  assert.equal(cancel.hidden, true);
  assert.equal(p.text('phase'), 'Canceling…', 'aborting is not yet confirmation');
  assert.equal(p.elements.get('progressPanel').hidden, true, 'a canceled upload closes its progress at once');
  assert.equal(p.elements.get('btn').disabled, false);
  await p.reply('uploading', 5000, 4000); // A late answer to the aborted poll.
  assert.equal(p.text('phase'), 'Canceling…');
  assert.equal(p.timers.size, 0, 'progress polling stopped');

  // The server is asked to cancel the upload, and then for its final state.
  const id = p.xhr.headers['X-Upload-ID'];
  const request = cancelRequest(p);
  assert.equal(request.url, '/upload-progress?id=' + id);
  assert.equal(request.options.body.get('action'), 'cancel');
  assert.equal(request.options.body.get('csrf_token'), 'a'.repeat(43));
  assert.equal(request.options.keepalive, true);
  await answer(request, 204);
  assert.equal(p.refreshes.length, 0, 'the file list waits for the outcome');
  const final = p.requests.find(request => !request.settled && request.url === '/upload-progress?id=' + id);
  await answer(final, 200, {state: 'canceled', bytes_received: 0});
  assert.equal(p.text('phase'), 'Canceled. Nothing from this upload was saved.');
  await p.settle();
  assert.equal(p.refreshes.length, 1);

  // A finished upload hides the button too.
  const done = page();
  done.finish(200);
  assert.equal(done.elements.get('cancelUpload').hidden, true);
});

test('a cancel that came too late, or was not confirmed, says so', async () => {
  const saved = page();
  saved.elements.get('cancelUpload').dispatch('click');
  await answer(cancelRequest(saved), 409);
  assert.equal(saved.text('phase'), 'The upload finished before it could be canceled, so its files were saved.');

  const stopping = page();
  stopping.elements.get('cancelUpload').dispatch('click');
  await answer(cancelRequest(stopping), 204);
  const final = stopping.requests.filter(request => !request.settled && request.options?.method !== 'POST' && request.url.includes('id=')).at(-1);
  await answer(final, 200, {state: 'complete', bytes_received: 10000});
  assert.match(stopping.text('phase'), /finished before it could be canceled/);

  const unknown = page();
  unknown.elements.get('cancelUpload').dispatch('click');
  await answer(cancelRequest(unknown), 500);
  assert.equal(unknown.text('phase'), 'Canceled, but the server did not confirm it. Check the file list.');
});

test('a late answer about a canceled upload does not overwrite a newer one', async () => {
  const p = page();
  p.elements.get('cancelUpload').dispatch('click');
  const request = cancelRequest(p);
  p.select(10);
  p.elements.get('btn').dispatch('click');
  assert.equal(p.text('phase'), 'Connecting to server');
  await answer(request, 409);
  assert.equal(p.text('phase'), 'Connecting to server');
});

test('Cancel on the upload link page asks its link to stop the upload', () => {
  const p = page(10000, {settings: {remainingBytes: undefined, csrfToken: undefined, directory: undefined, uploadUrl: '/u/link-token', progressUrl: '/u/link-token/progress'}, missing: ['fileList', 'explorerMessage', 'storageUsed', 'storageQuota', 'storageRemaining']});
  p.elements.get('cancelUpload').dispatch('click');
  const stop = p.requests.find(request => request.options?.method === 'POST');
  assert.equal(stop.url, '/u/link-token/progress?id=' + p.xhr.headers['X-Upload-ID']);
  assert.equal(stop.options.body.get('action'), 'cancel');
  assert.equal(stop.options.body.has('csrf_token'), false);
});
