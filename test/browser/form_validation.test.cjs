const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const {test} = require('node:test');

const script = fs.readFileSync(path.join(__dirname, '../../web/static/validation.js'), 'utf8');

function page({popover = true} = {}) {
  const listeners = new Map();
  const windowListeners = new Map();
  const callbacks = [];
  const body = {append(child) { child.parent = this; }};
  const tooltip = {
    id: 'validationTooltip', hidden: true, dataset: {},
    style: {setProperty(name, value) { this[name] = value; }},
    getBoundingClientRect() { return {width: 220, height: 42}; }
  };
  if (popover) {
    tooltip.showPopover = function () { this.open = true; };
    tooltip.hidePopover = function () { this.open = false; };
  }
  const message = {textContent: ''};
  let focused = null;
  function field(options = {}) {
    return {
      dataset: {requiredMessage: 'Enter a folder name.'},
      validity: {valueMissing: true, valid: false},
      validationMessage: 'Native validation message',
      attributes: {}, isConnected: true,
      rect: {left: 100, top: 100, bottom: 140, right: 240, width: 140},
      matches() { return true; },
      closest() { return this.dialog || null; },
      getBoundingClientRect() { return this.rect; },
      getAttribute(name) { return this.attributes[name] || null; },
      setAttribute(name, value) { this.attributes[name] = value; },
      removeAttribute(name) { delete this.attributes[name]; },
      focus() { focused = this; },
      ...options
    };
  }
  const input = field();
  const window = {
    innerWidth: 800, innerHeight: 600,
    addEventListener(name, callback) { windowListeners.set(name, callback); }
  };
  vm.runInNewContext(script, {
    document: {
      body,
      getElementById(id) { return id === tooltip.id ? tooltip : message; },
      addEventListener(name, callback, capture) { listeners.set(name, {callback, capture}); }
    },
    window,
    setTimeout(callback) { callbacks.push(callback); }
  });
  function dispatch(type, target = input, properties = {}) {
    const event = {target, prevented: false, preventDefault() { this.prevented = true; }, ...properties};
    listeners.get(type).callback(event);
    return event;
  }
  return {
    input, tooltip, message, body, window, listeners, field, dispatch,
    focused() { return focused; },
    flush() { while (callbacks.length) callbacks.shift()(); },
    position(type = 'resize') { windowListeners.get(type)(); }
  };
}

test('folder feedback replaces the native popup, links its description, and clears on editing', () => {
  const p = page();
  p.input.attributes['aria-describedby'] = 'folderHelp';
  assert.equal(p.listeners.get('invalid').capture, true); // Invalid events do not bubble.
  assert.equal(p.dispatch('invalid').prevented, true);
  p.flush();
  assert.equal(p.tooltip.hidden, false);
  assert.equal(p.tooltip.open, true);
  assert.equal(p.tooltip.parent, p.body);
  assert.equal(p.message.textContent, 'Enter a folder name.');
  assert.equal(p.input.attributes['aria-invalid'], 'true');
  assert.equal(p.input.attributes['aria-describedby'], 'folderHelp validationTooltip');
  assert.equal(p.focused(), p.input);
  p.dispatch('input');
  assert.equal(p.tooltip.hidden, true);
  assert.equal(p.tooltip.open, false);
  assert.equal(p.message.textContent, '');
  assert.equal(p.input.attributes['aria-invalid'], undefined);
  assert.equal(p.input.attributes['aria-describedby'], 'folderHelp');
});

test('all fields share feedback, keep constraint messages, and focus the first invalid field', () => {
  const p = page();
  const password = p.field({dataset: {}, validity: {valueMissing: false}, validationMessage: 'Use at least 8 characters.'});
  p.dispatch('invalid');
  p.dispatch('invalid', password);
  p.flush();
  assert.equal(p.focused(), p.input);
  assert.equal(p.message.textContent, 'Enter a folder name.');
  assert.equal(password.attributes['aria-invalid'], 'true');
  p.dispatch('input');
  assert.equal(p.dispatch('invalid', password).prevented, true);
  p.flush();
  assert.equal(p.focused(), password);
  assert.equal(p.message.textContent, 'Use at least 8 characters.');
  assert.equal(password.attributes['aria-describedby'], 'validationTooltip');
});

test('Escape, focus changes, and dialog close dismiss feedback without changing field validity', () => {
  for (const action of ['keydown', 'focusout', 'close']) {
    const p = page();
    const dialog = {open: true, append(child) { child.parent = this; }, contains(input) { return input === p.input; }};
    p.input.dialog = dialog;
    p.dispatch('invalid');
    p.flush();
    assert.equal(p.tooltip.parent, dialog);
    const event = p.dispatch(action, action === 'close' ? dialog : p.input, {key: 'Escape'});
    assert.equal(event.prevented, action === 'keydown');
    assert.equal(p.tooltip.hidden, true);
    assert.equal(p.input.attributes['aria-invalid'], 'true');
    assert.equal(p.input.attributes['aria-describedby'], undefined);
  }
});

test('feedback stays on screen, flips above low fields, and follows scrolling', () => {
  const p = page();
  p.input.rect = {left: 740, top: 520, bottom: 560, right: 790, width: 50};
  p.dispatch('invalid');
  p.flush();
  assert.equal(p.tooltip.style.left, '568px');
  assert.equal(p.tooltip.style.top, '470px');
  assert.equal(p.tooltip.dataset.placement, 'above');
  p.input.rect = {left: 10, top: 50, bottom: 90, right: 60, width: 50};
  p.position('scroll');
  assert.equal(p.tooltip.style.left, '12px');
  assert.equal(p.tooltip.style.top, '98px');
  assert.equal(p.tooltip.dataset.placement, 'below');
  p.input.rect.top = -100;
  p.input.rect.bottom = -60;
  p.position('scroll');
  assert.equal(p.tooltip.hidden, true);
});

test('stale fields do not reopen feedback, and older browsers still show it', () => {
  for (const options of [{isConnected: false}, {disabled: true}, {validity: {valid: true}}, {dialog: {open: false}}]) {
    const p = page();
    Object.assign(p.input, options);
    p.dispatch('invalid');
    p.flush();
    assert.equal(p.tooltip.hidden, true);
  }
  const p = page({popover: false});
  p.dispatch('invalid');
  p.flush();
  assert.equal(p.tooltip.hidden, false);
  p.dispatch('input');
  assert.equal(p.tooltip.hidden, true);
});
