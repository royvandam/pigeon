(function () {
  const dialog = document.getElementById('actionDialog');
  const panel = document.getElementById('shareControls');
  const input = document.getElementById('shareURL');
  const status = document.getElementById('shareStatus');
  const create = document.getElementById('shareCreate');
  const revoke = document.getElementById('shareRevoke');
  const copy = document.getElementById('shareCopy');
  const csrf = dialog.dataset.csrfToken;
  let path = '';
  let version = 0;
  let controller;

  function updateControls(busy) {
    const shared = input.value !== '';
    create.hidden = shared;
    revoke.hidden = !shared;
    create.disabled = revoke.disabled = busy;
    copy.disabled = busy || !shared;
  }

  async function request(method, action = '') {
    controller?.abort();
    controller = new AbortController();
    const current = ++version;
    updateControls(true);
    status.textContent = method === 'GET' ? 'Loading link…' : (action === 'create' ? 'Creating link…' : 'Revoking link…');
    try {
      const options = {method, credentials: 'same-origin', signal: controller.signal, cache: 'no-store'};
      let endpoint = '/shares?path=' + encodeURIComponent(path);
      if (method === 'POST') {
        endpoint = '/shares';
        options.body = new URLSearchParams({action, path, csrf_token: csrf});
      }
      const response = await fetch(endpoint, options);
      if (!response.ok) {
        if (response.status === 401) throw new Error('Your session has expired. Sign in and try again.');
        throw new Error((await response.text()).trim() || 'Could not update the share link. Try again.');
      }
      const data = await response.json();
      if (current !== version || !dialog.open || panel.hidden) return;
      input.value = data.url ? new URL(data.url, window.location.origin).href : '';
      status.textContent = input.value ? 'Anyone with this link can download without signing in.' : (action === 'revoke' ? 'Link revoked. The previous URL no longer works.' : 'No active link. Choose Create link to enable sharing.');
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
    input.value = '';
    request('GET');
  });
  dialog.addEventListener('close', () => {
    if (dialog.open) return;
    ++version;
    controller?.abort();
  });
  create.addEventListener('click', () => request('POST', 'create'));
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
