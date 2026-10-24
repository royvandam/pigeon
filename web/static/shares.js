(function () {
  const dialog = document.getElementById('actionDialog');
  const panel = document.getElementById('shareControls');
  const uploadSection = document.getElementById('uploadLinkControls');
  const csrf = dialog.dataset.csrfToken;
  let path = '';

  // Each kind of link has its own controls in the dialog: a folder can have a
  // download link and an upload link at the same time.
  function linkControls(kind, prefix, messages) {
    const input = document.getElementById(prefix + 'URL');
    const status = document.getElementById(prefix + 'Status');
    const lifetime = document.getElementById(prefix + 'Lifetime');
    const create = document.getElementById(prefix + 'Create');
    const replace = document.getElementById(prefix + 'Replace');
    const revoke = document.getElementById(prefix + 'Revoke');
    const copy = document.getElementById(prefix + 'Copy');
    // The server stores only a hash of each link, so an existing link's URL is
    // known only in the response that created it.
    let active = false;
    let expires = null;
    let version = 0;
    let controller;

    function updateControls(busy) {
      const shown = input.value !== '';
      create.hidden = active;
      replace.hidden = !active || shown;
      revoke.hidden = !active;
      create.disabled = replace.disabled = revoke.disabled = lifetime.disabled = busy;
      copy.disabled = busy || !shown;
    }

    function describe(action) {
      const until = expires ? ' It works until ' + new Date(expires).toLocaleString() + '.' : '';
      if (input.value) return 'Copy this link now; it will not be shown again. ' + messages.anyone + until;
      if (active) return 'A link is active.' + until + ' Its URL was shown only when it was created. Replace it to get a new URL; the current one will stop working.';
      if (action === 'revoke') return 'Link revoked. The previous URL no longer works.';
      return 'No active link. ' + messages.none;
    }

    function visible() {
      return dialog.open && !panel.hidden && !(kind === 'upload' && uploadSection.hidden);
    }

    async function request(method, action = '', progress = '') {
      controller?.abort();
      controller = new AbortController();
      const current = ++version;
      updateControls(true);
      status.textContent = progress || (method === 'GET' ? 'Loading link…' : (action === 'create' ? 'Creating link…' : 'Revoking link…'));
      try {
        const options = {method, credentials: 'same-origin', signal: controller.signal, cache: 'no-store', headers: {'X-Requested-With': 'XMLHttpRequest'}};
        let endpoint = '/shares?' + new URLSearchParams({path, kind});
        if (method === 'POST') {
          endpoint = '/shares';
          options.body = new URLSearchParams({action, path, kind, lifetime: lifetime.value, csrf_token: csrf});
        }
        const response = await fetch(endpoint, options);
        if (!response.ok) {
          if (response.status === 401) throw new Error('Your session has expired. Reload the page to sign in again.');
          throw new Error((await response.text()).trim() || 'Could not update the share link. Try again.');
        }
        const data = await response.json();
        if (current !== version || !visible()) return;
        active = Boolean(data.active || data.url);
        expires = active ? data.expires_at || null : null;
        input.value = data.url ? new URL(data.url, window.location.origin).href : '';
        input.placeholder = active && !input.value ? 'Hidden after creation' : '';
        status.textContent = describe(action);
        if (action === 'create') {
          input.focus();
          input.select();
        }
      } catch (error) {
        if (current === version && visible() && error.name !== 'AbortError') status.textContent = error.message;
      } finally {
        if (current === version) updateControls(false);
      }
    }

    create.addEventListener('click', () => request('POST', 'create'));
    replace.addEventListener('click', () => request('POST', 'create', 'Replacing link…'));
    revoke.addEventListener('click', () => request('POST', 'revoke'));
    copy.addEventListener('click', async () => {
      const current = version;
      input.focus();
      input.select();
      try {
        await navigator.clipboard.writeText(input.value);
        if (current === version && visible()) status.textContent = 'Link copied.';
      } catch {
        if (current === version && visible()) status.textContent = 'The link is selected. Copy it from the field.';
      }
    });

    return {
      load() {
        active = false;
        expires = null;
        input.value = input.placeholder = '';
        request('GET');
      },
      stop() {
        ++version;
        controller?.abort();
      }
    };
  }

  const download = linkControls('download', 'share', {
    anyone: 'Anyone with it can download without signing in.',
    none: 'Choose Create link to enable sharing.'
  });
  const upload = linkControls('upload', 'uploadLink', {
    anyone: 'Anyone with it can upload files into this folder without signing in, but cannot see what it holds.',
    none: 'Choose Create link to let others upload into this folder.'
  });

  dialog.addEventListener('share', event => {
    path = event.detail.path;
    download.load();
    // Only folders take uploads.
    uploadSection.hidden = !event.detail.directory;
    if (event.detail.directory) upload.load();
    else upload.stop();
  });
  dialog.addEventListener('close', () => {
    if (dialog.open) return;
    download.stop();
    upload.stop();
  });
})();
