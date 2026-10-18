(function () {
  const settings = document.getElementById('uploadPanel').dataset;
  let REMAINING_BYTES = Number(settings.remainingBytes);
  // The server charges each file its size in whole allocation units (at least one) plus
  // one unit per new folder. Folders are not known here, so this is a minimum.
  const ALLOCATION_UNIT = Number(settings.allocationUnit) || 4096;
  const CSRF_TOKEN = settings.csrfToken;
  const CURRENT_DIRECTORY = settings.directory;

  // Marks requests as coming from the browser, so an expired session gets a plain
  // 401 instead of a Basic challenge and the browser's own sign-in prompt.
  const BROWSER_HEADERS = {'X-Requested-With': 'XMLHttpRequest'};
  const SESSION_EXPIRED = 'Your session has expired. Reload the page to sign in again.';

  const fileList = document.getElementById('fileList');
  const explorerMessage = document.getElementById('explorerMessage');
  async function refreshFiles() {
    try {
      const response = await fetch('/files?dir=' + encodeURIComponent(CURRENT_DIRECTORY), {credentials: 'same-origin', cache: 'no-store', headers: BROWSER_HEADERS});
      if (response.status === 401) throw new Error(SESSION_EXPIRED);
      if (!response.ok) throw new Error('file list unavailable');
      fileList.innerHTML = await response.text();
      explorerMessage.textContent = '';
    } catch (error) {
      explorerMessage.textContent = error.message === SESSION_EXPIRED ? SESSION_EXPIRED : 'The file list could not be refreshed. Reload the page to see your files.';
    }
  }

  const btn = document.getElementById('btn');
  const fileInput = document.getElementById('file');
  const directoryInput = document.getElementById('directory');
  const supportsDirectories = 'webkitdirectory' in directoryInput;
  let selectedFiles = [];

  const pct = document.getElementById('pct');
  const spd = document.getElementById('spd');
  const up = document.getElementById('up');
  const tot = document.getElementById('tot');
  const eta = document.getElementById('eta');

  const bar = document.getElementById('bar');
  const phase = document.getElementById('phase');

  const fileName = document.getElementById('fileName');
  const fileCount = document.getElementById('fileCount');
  const fileSize = document.getElementById('fileSize');

  function humanBytes(n) {
    const units = ["B","KB","MB","GB","TB"];
    let i = 0;
    let v = n;
    while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
    return (i === 0 ? v.toFixed(0) : v.toFixed(2)) + " " + units[i];
  }

  function setDisabled(disabled) {
    btn.disabled = disabled || selectedFiles.length === 0 || REMAINING_BYTES < ALLOCATION_UNIT || chargedSize() > REMAINING_BYTES;
    fileInput.disabled = disabled;
    directoryInput.disabled = disabled || !supportsDirectories;
  }

  function selectedSize() {
    return selectedFiles.reduce((total, file) => total + file.size, 0);
  }

  function chargedSize() {
    return selectedFiles.reduce((total, file) => total + Math.max(1, Math.ceil(file.size / ALLOCATION_UNIT)) * ALLOCATION_UNIT, 0);
  }

  function showQuotaError(charged) {
    phase.textContent = 'Not enough storage: ' + humanBytes(charged) + ' needed, ' + humanBytes(REMAINING_BYTES) + ' available.';
  }

  function setProgress(percent) {
    const p = Math.max(0, Math.min(100, percent));
    pct.textContent = p + "%";
    bar.style.width = p + "%";
    document.getElementById('progress').setAttribute('aria-valuenow', String(p));
  }

  function humanDuration(seconds) {
    const value = Math.max(1, Math.ceil(seconds));
    if (value < 60) return value + 's';
    if (value < 3600) return Math.floor(value / 60) + 'm ' + (value % 60) + 's';
    return Math.floor(value / 3600) + 'h ' + Math.floor((value % 3600) / 60) + 'm';
  }

  setDisabled(false);
  if (!supportsDirectories) {
    document.getElementById('directoryHelp').textContent = 'Directory selection is unavailable in this browser. Use Choose files instead.';
  }

  function updateStorage(xhr) {
    const remaining = xhr.getResponseHeader('X-Quota-Remaining');
    if (remaining === null) return;
    REMAINING_BYTES = Number(remaining);
    document.getElementById('storageRemaining').textContent = humanBytes(REMAINING_BYTES);
    document.getElementById('storageUsed').textContent = humanBytes(Number(xhr.getResponseHeader('X-Quota-Used')));
    document.getElementById('storageQuota').textContent = humanBytes(Number(xhr.getResponseHeader('X-Quota-Limit')));
  }

  function selectFiles(input, otherInput) {
    otherInput.value = '';
    selectedFiles = Array.from(input.files || []);
    const size = selectedSize();

    if (selectedFiles.length === 0) {
      fileName.textContent = 'none';
    } else if (input === directoryInput) {
      fileName.textContent = selectedFiles[0].webkitRelativePath.split('/')[0];
    } else {
      fileName.textContent = selectedFiles.length === 1 ? selectedFiles[0].name : selectedFiles[0].name + ' and ' + (selectedFiles.length - 1) + ' more';
    }
    fileCount.textContent = selectedFiles.length + (selectedFiles.length === 1 ? ' file' : ' files');
    fileSize.textContent = humanBytes(size);
    tot.textContent = humanBytes(size);
    setProgress(0);
    spd.textContent = '0 B/s';
    eta.textContent = '—';
    up.textContent = '0 B';
    phase.textContent = 'Idle';
    setDisabled(false);

    if (chargedSize() > REMAINING_BYTES) {
      showQuotaError(chargedSize());
    }
  }

  fileInput.addEventListener('change', () => selectFiles(fileInput, directoryInput));
  directoryInput.addEventListener('change', () => selectFiles(directoryInput, fileInput));

  btn.addEventListener('click', () => {
    const files = selectedFiles.slice();
    const totalSize = selectedSize();

    if (files.length === 0) { phase.textContent = "Choose files or a directory containing files."; return; }

    if (chargedSize() > REMAINING_BYTES) {
      showQuotaError(chargedSize());
      return;
    }

    setDisabled(true);

    setProgress(0);
    spd.textContent = "0 B/s";
    eta.textContent = "Calculating…";
    up.textContent = "0 B";
    tot.textContent = humanBytes(totalSize);

    phase.textContent = "Connecting to server";

    const form = new FormData();
    for (const file of files) {
      form.append("file", file, file.webkitRelativePath || file.name);
    }

    const xhr = new XMLHttpRequest();
    xhr.open("POST", "/upload?dir=" + encodeURIComponent(CURRENT_DIRECTORY), true);

    xhr.setRequestHeader("X-CSRF-Token", CSRF_TOKEN);
    xhr.setRequestHeader("X-Requested-With", BROWSER_HEADERS['X-Requested-With']);
    // getRandomValues also works on local HTTP; randomUUID requires HTTPS.
    const uploadID = Array.from(crypto.getRandomValues(new Uint8Array(16)), byte => byte.toString(16).padStart(2, '0')).join('');
    xhr.setRequestHeader("X-Upload-ID", uploadID);

    const tStart = performance.now();
    const samples = [{time: tStart, bytes: 0}];
    let stopped = false;
    let progressSeen = false;
    let pollTimer = null;
    let progressRequest = null;

    function stopProgress() {
      stopped = true;
      if (pollTimer !== null) clearTimeout(pollTimer);
      if (progressRequest) progressRequest.abort();
    }

    function progressUnavailable() {
      phase.textContent = "Uploading (progress unavailable)";
      spd.textContent = "—";
      eta.textContent = "Unavailable";
    }

    function showProgress(snapshot) {
      const now = performance.now();
      const received = Math.min(totalSize, Math.max(0, Number(snapshot.bytes_received)));
      samples.push({time: now, bytes: received});
      // A short rolling window smooths the estimate and also lets it fall to
      // zero when the server stops receiving data, instead of counting down.
      while (samples.length > 2 && samples[1].time < now - 5000) samples.shift();
      const first = samples[0];
      const bps = (received - first.bytes) / Math.max((now - first.time) / 1000, 0.001);

      up.textContent = humanBytes(received);
      setProgress(totalSize > 0 ? Math.min(99, Math.floor(received / totalSize * 100)) : 0);
      spd.textContent = humanBytes(Math.max(0, bps)) + "/s";

      if (snapshot.state === 'queued') {
        phase.textContent = "Waiting for another upload";
        eta.textContent = "Waiting";
      } else if (snapshot.state === 'preparing') {
        phase.textContent = "Checking storage";
        eta.textContent = "Calculating…";
      } else if (snapshot.state === 'failed') {
        phase.textContent = "Upload failed";
        eta.textContent = "—";
        spd.textContent = "—";
      } else if (snapshot.state === 'finalizing' || snapshot.state === 'complete' || (totalSize > 0 && received === totalSize)) {
        phase.textContent = "Finishing upload";
        eta.textContent = "Finishing…";
        spd.textContent = "—";
      } else {
        phase.textContent = "Uploading";
        eta.textContent = bps > 0 ? '≈ ' + humanDuration((totalSize - received) / bps) : (received > 0 ? 'Waiting for data' : 'Calculating…');
      }
    }

    async function pollProgress() {
      if (stopped || progressRequest) return;
      if (pollTimer !== null) {
        clearTimeout(pollTimer);
        pollTimer = null;
      }
      const controller = new AbortController();
      progressRequest = controller;
      const timeout = setTimeout(() => controller.abort(), 5000);
      let terminal = false;
      try {
        const response = await fetch('/upload-progress?id=' + encodeURIComponent(uploadID), {
          credentials: 'same-origin', cache: 'no-store', signal: controller.signal, headers: BROWSER_HEADERS
        });
        if (stopped) return;
        if (response.ok) {
          const snapshot = await response.json();
          if (stopped) return;
          progressSeen = true;
          showProgress(snapshot);
          terminal = snapshot.state === 'complete' || snapshot.state === 'failed';
        } else if (response.status === 404 && !progressSeen) {
          // A buffering proxy may not have forwarded the upload request yet.
          phase.textContent = "Connecting to server";
          eta.textContent = "Calculating…";
        } else {
          progressUnavailable();
        }
      } catch (error) {
        if (!stopped) progressUnavailable();
      } finally {
        clearTimeout(timeout);
        progressRequest = null;
        if (!stopped && !terminal) pollTimer = setTimeout(pollProgress, 750);
      }
    }

    // Browser send completion does not mean the server has received or saved
    // the files. It only prompts another server progress check.
    xhr.upload.onload = pollProgress;

    xhr.onload = () => {
      stopProgress();

      updateStorage(xhr);

      const tEnd = performance.now();
      const secs = (tEnd - tStart) / 1000;
      const avgBps = totalSize / Math.max(secs, 0.001);

      if (xhr.status >= 200 && xhr.status < 300) {
        setProgress(100);
        up.textContent = humanBytes(totalSize);
        eta.textContent = "0s";
        spd.textContent = humanBytes(avgBps) + "/s avg";
        phase.textContent = "Done";
        refreshFiles();
      } else {
        eta.textContent = "—";
        spd.textContent = "—";
        phase.textContent = 'Upload failed: ' + (xhr.status === 401 ? SESSION_EXPIRED : (xhr.responseText.trim() || 'Please try again.'));
      }

      setDisabled(false);
    };

    xhr.onerror = () => {
      stopProgress();
      eta.textContent = "—";
      spd.textContent = "—";
      phase.textContent = "Network error. Please try again.";
      setDisabled(false);
    };

    xhr.onabort = () => {
      stopProgress();
      eta.textContent = "—";
      spd.textContent = "—";
      phase.textContent = "Canceled";
      setDisabled(false);
    };

    xhr.send(form);
    pollProgress();
  });
})();
