const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const {test} = require('node:test');

const html = fs.readFileSync(path.join(__dirname, '../templates/validation.html'), 'utf8');
const script = html.match(/<script>\s*([\s\S]*?)<\/script>/)[1];

function page() {
  const listeners = new Map();
  const error = {hidden: true, textContent: ''};
  const input = {
    dataset: {validationError: 'error', requiredMessage: 'Enter a folder name.'},
    validity: {valueMissing: true}, validationMessage: 'Native validation message',
    attributes: {}, focused: false,
    setAttribute(name, value) { this.attributes[name] = value; },
    removeAttribute(name) { delete this.attributes[name]; },
    focus() { this.focused = true; }
  };
  vm.runInNewContext(script, {document: {
    getElementById(id) { return id === 'error' ? error : null; },
    addEventListener(name, callback, capture) { listeners.set(name, {callback, capture}); }
  }});
  function dispatch(type, target = input) {
    const event = {target, prevented: false, preventDefault() { this.prevented = true; }};
    listeners.get(type).callback(event);
    return event;
  }
  return {input, error, listeners, dispatch};
}

test('empty folder names show an accessible inline error and suppress the native popup', () => {
  const p = page();
  assert.equal(p.listeners.get('invalid').capture, true); // Invalid events do not bubble.
  assert.equal(p.dispatch('invalid').prevented, true);
  assert.equal(p.error.hidden, false);
  assert.equal(p.error.textContent, 'Enter a folder name.');
  assert.equal(p.input.attributes['aria-invalid'], 'true');
  assert.equal(p.input.focused, true);
  p.dispatch('input');
  assert.equal(p.error.hidden, true);
  assert.equal(p.error.textContent, '');
  assert.equal(p.input.attributes['aria-invalid'], undefined);
});

test('other constraints keep their validation message and unrelated fields keep native validation', () => {
  const p = page();
  p.input.validity.valueMissing = false;
  p.input.validationMessage = 'Use at most 255 characters.';
  assert.equal(p.dispatch('invalid').prevented, true);
  assert.equal(p.error.textContent, 'Use at most 255 characters.');
  assert.equal(p.dispatch('invalid', {dataset: {}}).prevented, false);
});
