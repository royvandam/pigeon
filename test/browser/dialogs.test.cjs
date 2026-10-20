const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const {test} = require('node:test');

const shareHTML = fs.readFileSync(path.join(__dirname, '../../web/templates/shares.html'), 'utf8');
const html = fs.readFileSync(path.join(__dirname, '../../web/templates/dialog.html'), 'utf8') + shareHTML;
const script = fs.readFileSync(path.join(__dirname, '../../web/static/dialog.js'), 'utf8');
const shareScript = fs.readFileSync(path.join(__dirname, '../../web/static/shares.js'), 'utf8');

function page(options = {}) {
  const elements = new Map();
  const documentEvents = new Map();
  let focused = null;
  const requests = [];
  const clipboard = [];
  function element(id) {
    const events = new Map();
    return {
      id, value: '', textContent: '', attributes: {}, dataset: {}, children: [],
      setAttribute(name, value) { this.attributes[name] = value; },
      removeAttribute(name) { delete this.attributes[name]; },
      addEventListener(name, callback) { events.set(name, callback); },
      dispatch(name, event) { return events.get(name)?.(event); },
      replaceChildren() { this.children = []; },
      append(child) { this.children.push(child); },
      dispatchEvent(event) { events.get(event.type)?.(event); },
      focus() { focused = id; },
      select() { this.selected = true; },
      showModal() { this.open = true; },
      close() { this.open = false; this.dispatch('close'); }
    };
  }
  for (const match of html.matchAll(/id="([^"]+)"/g)) elements.set(match[1], element(match[1]));
  elements.get('actionDialog').dataset.csrfToken = 'a'.repeat(43);
  const document = {
    getElementById(id) { return elements.get(id); },
    createElement() { return element(null); },
    addEventListener(name, callback) { documentEvents.set(name, callback); }
  };
  function blocked() { throw new Error('Native browser dialogs must not be used'); }
  const context = vm.createContext({
    document, Event, CustomEvent, URL, URLSearchParams, AbortController,
    window: {alert: blocked, confirm: blocked, prompt: blocked, location: {origin: 'https://uploads.example'}},
    navigator: {clipboard: {async writeText(value) { if (options.clipboardBlocked) throw new Error('Clipboard unavailable'); clipboard.push(value); }}},
    fetch(url, options) {return new Promise(resolve => requests.push({url, options, resolve}));}
  });
  vm.runInContext(script, context);
  vm.runInContext(shareScript, context);
  function open(action, data = {}) {
    const button = {dataset: {dialogAction: action, name: 'report.txt', path: 'docs/report.txt', dir: 'docs', ...data}};
    const event = {target: {closest() { return button; }}, prevented: false, preventDefault() { this.prevented = true; }};
    documentEvents.get('click')(event);
    return event;
  }
  return {
    open, elements, requests, clipboard,
    input: elements.get('actionDialogValue'),
    dialog: elements.get('actionDialog'),
    form: elements.get('actionDialogForm'),
    fields() { return Object.fromEntries(elements.get('actionDialogFields').children.map(field => [field.name, field.value])); },
    focused() { return focused; },
    cancel() { elements.get('actionDialogCancel').dispatch('click'); },
    async reply(index, url = '', status = 200, active = Boolean(url)) {
      requests[index].resolve({ok: status === 200, status, async json() { return {url, active}; }, async text() { return 'Operation failed'; }});
      for (let i = 0; i < 10; i++) await Promise.resolve();
    },
    submit() { const event = {prevented: false, preventDefault() {this.prevented = true;}}; elements.get('actionDialogForm').dispatch('submit', event); return event; }
  };
}

test('rename and move share the edit dialog and preserve the destination folder', () => {
  const p = page();
  assert.equal(p.open('rename').prevented, true);
  assert.equal(p.dialog.open, true);
  assert.equal(p.form.attributes.action, '/files');
  assert.deepEqual(p.fields(), {action: 'rename', path: 'docs/report.txt', dir: 'docs'});
  assert.equal(p.input.name, 'name');
  assert.equal(p.input.value, 'report.txt');
  assert.equal(p.input.required, true);
  assert.equal(p.input.disabled, false);
  assert.equal(p.input.maxLength, 255);
  assert.equal(p.focused(), 'actionDialogValue');
  assert.equal(p.input.selected, true);
  assert.equal(p.input.attributes['aria-describedby'], undefined);
  assert.equal(p.elements.get('actionDialogDescription').textContent, '');
  p.cancel();
  assert.equal(p.dialog.open, false);
  p.open('move');
  assert.deepEqual(p.fields(), {action: 'move', path: 'docs/report.txt', dir: 'docs'});
  assert.equal(p.input.name, 'destination');
  assert.equal(p.input.value, 'docs');
  assert.equal(p.input.required, false);
  assert.equal(p.input.maxLength, 4095);
  assert.equal(p.elements.get('actionDialogHelp').hidden, false);
  assert.equal(p.input.attributes['aria-describedby'], 'actionDialogHelp');
  assert.equal(p.elements.get('actionDialogConfirmLabel').textContent, 'Save');
  p.cancel();
  p.open('move', {dir: ''});
  assert.equal(p.input.value, '');
});

for (const directory of [false, true]) {
  test(`deleting a ${directory ? 'folder' : 'file'} uses a confirmation dialog with no active text field`, () => {
    const p = page();
    assert.equal(p.open('delete-file', {directory: String(directory)}).prevented, true);
    assert.deepEqual(p.fields(), {action: 'delete', path: 'docs/report.txt', dir: 'docs'});
    assert.equal(p.dialog.open, true);
    assert.equal(p.input.disabled, true);
    assert.equal(p.input.required, false);
    assert.equal(p.elements.get('actionDialogInputGroup').hidden, true);
    assert.equal(p.elements.get('actionDialogConfirmLabel').textContent, 'Delete');
    assert.equal(p.elements.get('actionDialogConfirm').dataset.danger, 'true');
    assert.equal(p.focused(), 'actionDialogCancel');
    assert.match(p.elements.get('actionDialogDescription').textContent, directory ? /everything inside it/ : /deletes the file/);
    p.cancel();
    assert.equal(p.dialog.open, false);
  });
}

test('user deletion shares confirmation handling and does not retain file fields', () => {
  const p = page();
  p.open('move');
  p.cancel();
  p.open('delete-user', {name: 'Alice', userId: '42'});
  assert.equal(p.form.attributes.action, '/admin');
  assert.deepEqual(p.fields(), {action: 'delete', user_id: '42'});
  assert.equal(p.elements.get('actionDialogTitle').textContent, 'Delete user "Alice"?');
  assert.match(p.elements.get('actionDialogDescription').textContent, /uploads will be preserved/);
  assert.equal(p.input.disabled, true);
  p.cancel();
  p.open('rename');
  assert.equal(p.form.attributes.action, '/files');
  assert.deepEqual(p.fields(), {action: 'rename', path: 'docs/report.txt', dir: 'docs'});
  assert.equal(p.input.disabled, false);
  assert.equal(p.input.required, true);
  assert.equal(p.elements.get('actionDialogInputGroup').hidden, false);
  assert.equal(p.elements.get('actionDialogConfirm').dataset.danger, 'false');
});

for (const self of [false, true]) {
  test(`password reset confirms ${self ? 'signing yourself out' : 'the selected user'} with Cancel focused`, () => {
    const p = page();
    p.open('move');
    p.cancel();
    assert.equal(p.open('reset-password', {name: 'Alice', userId: '42', self: String(self)}).prevented, true);
    assert.equal(p.dialog.open, true);
    assert.equal(p.form.attributes.action, '/admin');
    assert.deepEqual(p.fields(), {action: 'reset', user_id: '42'});
    assert.equal(p.elements.get('actionDialogTitle').textContent, 'Reset password for "Alice"?');
    const description = p.elements.get('actionDialogDescription').textContent;
    assert.match(description, /replaces the current password with a new four-word passphrase/);
    assert.match(description, self ? /You will be signed out on all devices/ : /This user will be signed out on all devices/);
    assert.match(description, /Copy the new passphrase from the result page/);
    assert.equal(p.elements.get('actionDialogConfirmLabel').textContent, 'Reset password');
    assert.equal(p.elements.get('actionDialogConfirmIcon').className, 'fa-solid fa-key mr-1');
    assert.equal(p.elements.get('actionDialogConfirm').dataset.danger, 'true');
    assert.equal(p.elements.get('actionDialogConfirm').disabled, false);
    assert.equal(p.input.disabled, true);
    assert.equal(p.input.required, false);
    assert.equal(p.elements.get('actionDialogInputGroup').hidden, true);
    assert.equal(p.focused(), 'actionDialogCancel');
    assert.equal(p.requests.length, 0);
    p.cancel();
    assert.equal(p.dialog.open, false);
    assert.equal(p.requests.length, 0);
    p.open('reset-password', {name: 'Bob', userId: '43'});
    assert.deepEqual(p.fields(), {action: 'reset', user_id: '43'});
    assert.equal(p.submit().prevented, false);
  });
}

test('password reset clears sharing controls and restores other dialog modes', () => {
  const p = page();
  p.open('share');
  p.cancel();
  p.open('reset-password', {name: 'Alice', userId: '42', self: 'true'});
  assert.equal(p.requests[0].options.signal.aborted, true);
  assert.equal(p.elements.get('shareControls').hidden, true);
  assert.equal(p.elements.get('actionDialogConfirm').hidden, false);
  assert.equal(p.elements.get('actionDialogConfirm').disabled, false);
  assert.equal(p.elements.get('actionDialogCancelLabel').textContent, 'Cancel');
  assert.equal(p.submit().prevented, false);
  p.cancel();
  p.open('rename');
  assert.deepEqual(p.fields(), {action: 'rename', path: 'docs/report.txt', dir: 'docs'});
  assert.equal(p.elements.get('actionDialogConfirmLabel').textContent, 'Save');
  assert.equal(p.elements.get('actionDialogConfirm').dataset.danger, 'false');
  assert.equal(p.input.disabled, false);
  assert.equal(p.input.required, true);
});

test('unsupported actions are ignored without opening or submitting the dialog', () => {
  for (const action of ['unknown', '__proto__', 'toString']) {
    const p = page();
    assert.equal(p.open(action).prevented, false);
    assert.equal(p.dialog.open, undefined);
    assert.deepEqual(p.fields(), {});
  }
});

test('account recycling explicitly confirms preserved uploads and resets other dialog modes', () => {
  const p = page();
  p.open('move');
  p.cancel();
  assert.equal(p.open('recycle-user', {name: 'Alice', userId: '42', role: 'User', quota: '2.00 GB'}).prevented, true);
  assert.equal(p.dialog.open, true);
  assert.equal(p.form.attributes.action, '/admin');
  assert.deepEqual(p.fields(), {action: 'recycle', user_id: '42'});
  assert.equal(p.elements.get('actionDialogTitle').textContent, 'Recycle account "Alice"?');
  assert.match(p.elements.get('actionDialogDescription').textContent, /preserved uploads/);
  assert.match(p.elements.get('actionDialogDescription').textContent, /role \(User\) and quota \(2.00 GB\)/);
  assert.match(p.elements.get('actionDialogDescription').textContent, /sessions, and share links will remain revoked/);
  assert.equal(p.input.disabled, true);
  assert.equal(p.input.required, false);
  assert.equal(p.elements.get('actionDialogInputGroup').hidden, true);
  assert.equal(p.elements.get('actionDialogConfirmLabel').textContent, 'Recycle account');
  assert.equal(p.elements.get('actionDialogConfirm').dataset.danger, 'false');
  assert.equal(p.focused(), 'actionDialogCancel');
  assert.equal(p.requests.length, 0);
  p.cancel();
  assert.equal(p.dialog.open, false);
  p.open('rename');
  assert.equal(p.form.attributes.action, '/files');
  assert.equal(p.input.disabled, false);
  assert.equal(p.input.required, true);
  assert.equal(p.elements.get('actionDialogConfirmLabel').textContent, 'Save');
});

test('purging an account confirms permanent upload deletion and resets recycled dialog state', () => {
  const p = page();
  p.open('recycle-user', {name: 'Alice', userId: '42', role: 'User', quota: '1.00 GB'});
  p.cancel();
  assert.equal(p.open('purge-user', {name: 'Alice', userId: '42'}).prevented, true);
  assert.equal(p.dialog.open, true);
  assert.equal(p.form.attributes.action, '/admin');
  assert.deepEqual(p.fields(), {action: 'purge', user_id: '42'});
  assert.equal(p.elements.get('actionDialogTitle').textContent, 'Purge account "Alice"?');
  assert.match(p.elements.get('actionDialogDescription').textContent, /permanently deletes the account and all its uploaded files and folders/);
  assert.match(p.elements.get('actionDialogDescription').textContent, /cannot be undone/);
  assert.equal(p.elements.get('actionDialogConfirmLabel').textContent, 'Purge account');
  assert.equal(p.elements.get('actionDialogConfirm').dataset.danger, 'true');
  assert.equal(p.input.disabled, true);
  assert.equal(p.input.required, false);
  assert.equal(p.elements.get('actionDialogInputGroup').hidden, true);
  assert.equal(p.focused(), 'actionDialogCancel');
  assert.equal(p.requests.length, 0);
  p.cancel();
  assert.equal(p.dialog.open, false);
  p.open('rename');
  assert.equal(p.input.disabled, false);
  assert.equal(p.elements.get('actionDialogConfirm').dataset.danger, 'false');
});

test('sharing explicitly creates, copies and revokes a link in the existing dialog', async () => {
  const p = page();
  assert.equal(p.open('share').prevented, true);
  assert.equal(p.dialog.open, true);
  assert.equal(p.input.disabled, true);
  assert.equal(p.elements.get('actionDialogConfirm').disabled, true);
  assert.equal(p.submit().prevented, true);
  assert.equal(p.requests.length, 1);
  assert.equal(p.requests[0].options.method, 'GET');
  assert.equal(p.requests[0].url, '/shares?path=docs%2Freport.txt');
  await p.reply(0);
  assert.equal(p.elements.get('shareCreate').hidden, false);
  p.elements.get('shareCreate').dispatch('click');
  assert.equal(p.requests[1].options.body.get('action'), 'create');
  assert.equal(p.requests[1].options.body.get('path'), 'docs/report.txt');
  assert.equal(p.requests[1].options.body.get('csrf_token'), 'a'.repeat(43));
  await p.reply(1, '/s/secret-token');
  assert.equal(p.elements.get('shareURL').value, 'https://uploads.example/s/secret-token');
  assert.match(p.elements.get('shareStatus').textContent, /will not be shown again/);
  assert.equal(p.elements.get('shareCreate').hidden, true);
  assert.equal(p.elements.get('shareReplace').hidden, true);
  assert.equal(p.elements.get('shareRevoke').hidden, false);
  await p.elements.get('shareCopy').dispatch('click');
  assert.deepEqual(p.clipboard, ['https://uploads.example/s/secret-token']);
  p.elements.get('shareRevoke').dispatch('click');
  assert.equal(p.requests[2].options.body.get('action'), 'revoke');
  await p.reply(2);
  assert.equal(p.elements.get('shareURL').value, '');
  assert.equal(p.elements.get('shareCopy').disabled, true);
  assert.equal(p.elements.get('shareCreate').hidden, false);
  assert.equal(p.elements.get('shareRevoke').hidden, true);
  assert.match(p.elements.get('shareStatus').textContent, /no longer works/);
  p.cancel();
  p.open('rename');
  assert.equal(p.elements.get('shareControls').hidden, true);
  assert.equal(p.elements.get('actionDialogConfirm').disabled, false);
  assert.equal(p.submit().prevented, false);
});

test('existing links stay hidden, can be replaced, and clipboard failures select the new link', async () => {
  const p = page({clipboardBlocked: true});
  p.open('share', {directory: 'true'});
  assert.match(p.elements.get('actionDialogDescription').textContent, /ZIP, including files added later/);
  await p.reply(0, '', 200, true);
  assert.equal(p.elements.get('shareURL').value, '');
  assert.equal(p.elements.get('shareURL').placeholder, 'Hidden after creation');
  assert.match(p.elements.get('shareStatus').textContent, /current one will stop working/);
  assert.equal(p.elements.get('shareCopy').disabled, true);
  assert.equal(p.elements.get('shareCreate').hidden, true);
  assert.equal(p.elements.get('shareReplace').hidden, false);
  assert.equal(p.elements.get('shareRevoke').hidden, false);
  p.elements.get('shareReplace').dispatch('click');
  assert.equal(p.elements.get('shareStatus').textContent, 'Replacing link…');
  assert.equal(p.requests[1].options.body.get('action'), 'create');
  await p.reply(1, '/s/replacement-token');
  assert.equal(p.elements.get('shareURL').value, 'https://uploads.example/s/replacement-token');
  assert.equal(p.elements.get('shareURL').placeholder, '');
  assert.equal(p.elements.get('shareReplace').hidden, true);
  await p.elements.get('shareCopy').dispatch('click');
  assert.equal(p.elements.get('shareURL').selected, true);
  assert.match(p.elements.get('shareStatus').textContent, /Copy it from the field/);
  assert.equal(p.requests.length, 2);
});

test('late sharing responses cannot overwrite a newly opened item or edit dialog', async () => {
  const p = page();
  p.open('share');
  p.cancel();
  assert.equal(p.requests[0].options.signal.aborted, true);
  p.open('share', {name:'other.txt', path:'other.txt'});
  await p.reply(1, '/s/new-token');
  await p.reply(0, '/s/old-token');
  assert.equal(p.elements.get('shareURL').value, 'https://uploads.example/s/new-token');
  p.cancel();
  p.open('share');
  p.cancel();
  p.open('rename');
  await p.reply(2, '/s/late-token');
  assert.equal(p.elements.get('shareControls').hidden, true);
  assert.equal(p.elements.get('actionDialogTitle').textContent, 'Rename "report.txt"');
});

test('share request failures keep the dialog usable and explain expired sessions', async () => {
  const p = page();
  p.open('share');
  assert.equal(p.requests[0].options.headers['X-Requested-With'], 'XMLHttpRequest');
  await p.reply(0, '', 401);
  assert.equal(p.elements.get('shareStatus').textContent, 'Your session has expired. Reload the page to sign in again.');
  assert.equal(p.elements.get('shareCreate').disabled, false);
  assert.equal(p.elements.get('shareCopy').disabled, true);
});
