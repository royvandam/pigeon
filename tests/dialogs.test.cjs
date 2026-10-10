const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const {test} = require('node:test');

const html = fs.readFileSync(path.join(__dirname, '../templates/dialog.html'), 'utf8');
const script = html.match(/<script>\s*([\s\S]*?)<\/script>/)[1];

function page() {
  const elements = new Map();
  const documentEvents = new Map();
  let focused = null;
  function element(id) {
    const events = new Map();
    return {
      id, value: '', textContent: '', attributes: {}, dataset: {}, children: [],
      setAttribute(name, value) { this.attributes[name] = value; },
      addEventListener(name, callback) { events.set(name, callback); },
      dispatch(name) { events.get(name)(); },
      replaceChildren() { this.children = []; },
      append(child) { this.children.push(child); },
      dispatchEvent() {},
      focus() { focused = id; },
      select() { this.selected = true; },
      showModal() { this.open = true; },
      close() { this.open = false; }
    };
  }
  for (const match of html.matchAll(/id="([^"]+)"/g)) elements.set(match[1], element(match[1]));
  const document = {
    getElementById(id) { return elements.get(id); },
    createElement() { return element(null); },
    addEventListener(name, callback) { documentEvents.set(name, callback); }
  };
  function blocked() { throw new Error('Native browser dialogs must not be used'); }
  vm.runInNewContext(script, {document, Event, window: {alert: blocked, confirm: blocked, prompt: blocked}});
  function open(action, data = {}) {
    const button = {dataset: {dialogAction: action, name: 'report.txt', path: 'docs/report.txt', dir: 'docs', ...data}};
    const event = {target: {closest() { return button; }}, prevented: false, preventDefault() { this.prevented = true; }};
    documentEvents.get('click')(event);
    return event;
  }
  return {
    open, elements,
    input: elements.get('actionDialogValue'),
    dialog: elements.get('actionDialog'),
    form: elements.get('actionDialogForm'),
    fields() { return Object.fromEntries(elements.get('actionDialogFields').children.map(field => [field.name, field.value])); },
    focused() { return focused; },
    cancel() { elements.get('actionDialogCancel').dispatch('click'); }
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

test('unsupported actions are ignored without opening or submitting the dialog', () => {
  const p = page();
  assert.equal(p.open('unknown').prevented, false);
  assert.equal(p.dialog.open, undefined);
  assert.deepEqual(p.fields(), {});
});
