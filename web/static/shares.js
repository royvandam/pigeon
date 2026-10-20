(function () {
  const dialog = document.getElementById('actionDialog');
  const panel = document.getElementById('shareControls');
  const input = document.getElementById('shareURL');
  const status = document.getElementById('shareStatus');
  const create = document.getElementById('shareCreate');
  const replace = document.getElementById('shareReplace');
  const revoke = document.getElementById('shareRevoke');
  const copy = document.getElementById('shareCopy');
  const csrf = dialog.dataset.csrfToken;
  let path = '';
  // The server stores only a hash of each link, so an existing link's URL is
  // known only in the response that created it.
  let active = false;
  let version = 0;
  let controller;

  function updateControls(busy) {
    const shown = input.value !== '';
    create.hidden = active;
    replace.hidden = !active || shown;
    revoke.hidden = !active;
    create.disabled = replace.disabled = revoke.disabled = busy;
    copy.disabled = busy || !shown;
  }

  function describe(action) {
    if (input.value) return 'Copy this link now; it will not be shown again. Anyone with it can download without signing in.';
    if (active) return 'A link is active. Its URL was shown only when it was created. Replace it to get a new URL; the current one will stop working.';
    if (action === 'revoke') return 'Link revoked. The previous URL no longer works.';
    return 'No active link. Choose Create link to enable sharing.';
  }

  async function request(method, action = '', progress = '') {
    controller?.abort();
    controller = new AbortController();
    const current = ++version;
    updateControls(true);
    status.textContent = progress || (method === 'GET' ? 'Loading link…' : (action === 'create' ? 'Creating link…' : 'Revoking link…'));
    try {
      const options = {method, credentials: 'same-origin', signal: controller.signal, cache: 'no-store', headers: {'X-Requested-With': 'XMLHttpRequest'}};
      let endpoint = '/shares?path=' + encodeURIComponent(path);
      if (method === 'POST') {
        endpoint = '/shares';
        options.body = new URLSearchParams({action, path, csrf_token: csrf});
      }
      const response = await fetch(endpoint, options);
      if (!response.ok) {
        if (response.status === 401) throw new Error('Your session has expired. Reload the page to sign in again.');
        throw new Error((await response.text()).trim() || 'Could not update the share link. Try again.');
      }
      const data = await response.json();
      if (current !== version || !dialog.open || panel.hidden) return;
      active = Boolean(data.active || data.url);
      input.value = data.url ? new URL(data.url, window.location.origin).href : '';
      input.placeholder = active && !input.value ? 'Hidden after creation' : '';
      status.textContent = describe(action);
      if (action === 'create') {
        input.focus();
        input.select();
      }
    } catch (error) {
      if (current === version && dialog.open && !panel.hidden && error.name !== 'AbortError') status.textContent = error.message;
    } finally {
      if (current === version) updateControls(false);
    }
  }

  dialog.addEventListener('share', event => {
    path = event.detail.path;
    active = false;
    input.value = input.placeholder = '';
    request('GET');
  });
  dialog.addEventListener('close', () => {
    if (dialog.open) return;
    ++version;
    controller?.abort();
  });
  create.addEventListener('click', () => request('POST', 'create'));
  replace.addEventListener('click', () => request('POST', 'create', 'Replacing link…'));
  revoke.addEventListener('click', () => request('POST', 'revoke'));
  copy.addEventListener('click', async () => {
    const current = version;
    input.focus();
    input.select();
    try {
      await navigator.clipboard.writeText(input.value);
      if (current === version && dialog.open && !panel.hidden) status.textContent = 'Link copied.';
    } catch {
      if (current === version && dialog.open && !panel.hidden) status.textContent = 'The link is selected. Copy it from the field.';
    }
  });
})();
