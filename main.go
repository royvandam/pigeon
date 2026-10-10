package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type config struct {
	dir      string
	maxBytes int64
	password string
	listen   string
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Truncate(time.Millisecond))
	})
}

func main() {
	var (
		dir      = flag.String("dir", "./uploads", "Directory where uploaded files are written")
		maxSize  = flag.String("max", "200MB", "Maximum upload size (e.g. 10MB, 1GB, 500K)")
		password = flag.String("password", "", "Shared password required to upload")
		listen   = flag.String("listen", "0.0.0.0:8080", "Listen address")
	)
	flag.Parse()

	if *password == "" {
		log.Fatal("missing required flag: -password")
	}

	maxBytes, err := parseSize(*maxSize)
	if err != nil {
		log.Fatalf("invalid -max: %v", err)
	}
	if maxBytes <= 0 {
		log.Fatal("-max must be > 0")
	}

	if err := os.MkdirAll(*dir, 0o755); err != nil {
		log.Fatalf("failed to create dir %q: %v", *dir, err)
	}

	cfg := config{
		dir:      *dir,
		maxBytes: maxBytes,
		password: *password,
		listen:   *listen,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", cfg.handleIndex)
	mux.HandleFunc("/upload", cfg.handleUpload)

	srv := &http.Server{
		Addr:              cfg.listen,
		Handler:           logRequests(mux),
		ReadTimeout:       0,
		WriteTimeout:      0,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("listening on http://%s", cfg.listen)
	log.Printf("upload dir: %s", cfg.dir)
	log.Printf("max upload: %d bytes", cfg.maxBytes)

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}

	_ = srv.Shutdown(context.Background())
}

func (cfg config) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := struct {
		MaxHuman string
		MaxBytes int64
	}{
		MaxHuman: humanBytes(cfg.maxBytes),
		MaxBytes: cfg.maxBytes,
	}
	if err := indexTmpl.Execute(w, data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (cfg config) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 1) Check password FIRST. Do not touch the body.
	if r.Header.Get("X-Upload-Password") != cfg.password {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// 2) Cap total body size (file + multipart overhead)
	const overhead = int64(10 << 20) // 10 MB safety margin
	r.Body = http.MaxBytesReader(w, r.Body, cfg.maxBytes+overhead)
	defer r.Body.Close()

	// 3) Parse multipart manually so we can stream
	ct := r.Header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(ct)
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		http.Error(w, "expected multipart form", http.StatusBadRequest)
		return
	}

	boundary := params["boundary"]
	if boundary == "" {
		http.Error(w, "missing multipart boundary", http.StatusBadRequest)
		return
	}

	mr := multipart.NewReader(r.Body, boundary)

	var (
		savedName string
		written   int64
	)

	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			http.Error(w, "multipart read error", http.StatusBadRequest)
			return
		}

		if part.FormName() != "file" {
			part.Close()
			continue
		}

		// 4) Prepare output file
		orig := part.FileName()
		safe := sanitizeFilename(orig)
		if safe == "" {
			safe = "upload.bin"
		}

		name := fmt.Sprintf(
			"%s_%s_%s",
			time.Now().UTC().Format("20060102T150405Z"),
			shortRandHex(6),
			safe,
		)

		path := filepath.Join(cfg.dir, name)
		out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
		if err != nil {
			part.Close()
			http.Error(w, "failed to create file", http.StatusInternalServerError)
			return
		}

		// 5) Stream with hard size cap
		limited := &io.LimitedReader{
			R: part,
			N: cfg.maxBytes + 1,
		}

		n, copyErr := io.Copy(out, limited)

		out.Close()
		part.Close()

		if copyErr != nil {
			os.Remove(path)
			http.Error(w, "upload failed", http.StatusBadRequest)
			return
		}

		// If we consumed maxBytes+1, the file is too big
		if limited.N == 0 {
			os.Remove(path)
			http.Error(
				w,
				fmt.Sprintf("file too large (limit %s)", humanBytes(cfg.maxBytes)),
				http.StatusRequestEntityTooLarge,
			)
			return
		}

		savedName = name
		written = n
		break
	}

	if savedName == "" {
		http.Error(w, "missing file", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "ok\nsaved_as=%s\nbytes=%d\n", savedName, written)
}


// ---------- helpers ----------

var sizeRe = regexp.MustCompile(`(?i)^\s*(\d+)\s*(b|kb|k|mb|m|gb|g|tb|t)\s*$`)

func parseSize(s string) (int64, error) {
	m := sizeRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, fmt.Errorf("expected like 10MB, 1GB, 500K, got %q", s)
	}
	var n int64
	_, err := fmt.Sscanf(m[1], "%d", &n)
	if err != nil {
		return 0, err
	}
	unit := strings.ToLower(m[2])

	mult := int64(1)
	switch unit {
	case "b":
		mult = 1
	case "k", "kb":
		mult = 1024
	case "m", "mb":
		mult = 1024 * 1024
	case "g", "gb":
		mult = 1024 * 1024 * 1024
	case "t", "tb":
		mult = 1024 * 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("unknown unit %q", unit)
	}
	return n * mult, nil
}

func humanBytes(b int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
		TB = 1024 * GB
	)
	switch {
	case b >= TB:
		return fmt.Sprintf("%.2f TB", float64(b)/float64(TB))
	case b >= GB:
		return fmt.Sprintf("%.2f GB", float64(b)/float64(GB))
	case b >= MB:
		return fmt.Sprintf("%.2f MB", float64(b)/float64(MB))
	case b >= KB:
		return fmt.Sprintf("%.2f KB", float64(b)/float64(KB))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

var filenameBad = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func sanitizeFilename(name string) string {
	name = filepath.Base(name)
	name = strings.TrimSpace(name)
	if name == "." || name == ".." {
		return ""
	}
	name = filenameBad.ReplaceAllString(name, "_")
	name = strings.Trim(name, "._-")
	if len(name) > 180 {
		name = name[:180]
	}
	return name
}

func shortRandHex(nbytes int) string {
	buf := make([]byte, nbytes)
	if _, err := rand.Read(buf); err != nil {
		// fallback; not security-critical for filenames
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

// ---------- HTML ----------

var indexTmpl = template.Must(template.New("index").Parse(`
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>Upload</title>

  <script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4"></script>
</head>

<body class="min-h-screen bg-zinc-950 text-zinc-100">
  <div class="mx-auto max-w-xl px-4 py-10">
    <div class="rounded-2xl border border-zinc-800 bg-zinc-900/40 p-6 shadow-sm">
      <div class="flex items-start justify-between gap-4">
        <div>
          <h1 class="text-xl font-semibold">Upload a file</h1>
          <p class="mt-1 text-sm text-zinc-400">Max upload: {{.MaxHuman}}</p>
        </div>
        <div class="text-xs text-zinc-500">
          Single file
        </div>
      </div>

      <div class="mt-6 space-y-4">
        <div>
          <label for="password" class="block text-sm font-medium text-zinc-200">Password</label>
          <input
            id="password"
            type="password"
            autocomplete="off"
            placeholder="Shared password"
            class="mt-2 w-full rounded-xl border border-zinc-800 bg-zinc-950/40 px-3 py-2 text-sm text-zinc-100 placeholder:text-zinc-600 focus:border-zinc-600 focus:outline-none focus:ring-2 focus:ring-zinc-600/30"
          />
        </div>

        <div>
          <label for="file" class="block text-sm font-medium text-zinc-200">File</label>
          <div class="mt-2 rounded-xl border border-dashed border-zinc-800 bg-zinc-950/20 p-4">
            <input
              id="file"
              type="file"
              class="block w-full text-sm text-zinc-200 file:mr-4 file:rounded-lg file:border-0 file:bg-zinc-800 file:px-3 file:py-2 file:text-sm file:font-medium file:text-zinc-100 hover:file:bg-zinc-700"
            />
            <div class="mt-3 text-xs text-zinc-500">
              Selected: <span id="fileName" class="text-zinc-300">none</span>
              <span class="mx-2 text-zinc-700">|</span>
              Size: <span id="fileSize" class="text-zinc-300">0 B</span>
            </div>
          </div>
        </div>

        <div class="flex items-center gap-3">
          <button
            id="btn"
            class="inline-flex items-center justify-center rounded-xl bg-zinc-100 px-4 py-2 text-sm font-semibold text-zinc-900 hover:bg-white disabled:cursor-not-allowed disabled:opacity-60"
          >
            Upload
          </button>

          <div class="text-xs text-zinc-500">
            <span id="phase">Idle</span>
          </div>
        </div>

        <div class="rounded-xl border border-zinc-800 bg-zinc-950/30 p-4">
          <div class="flex items-center justify-between text-xs text-zinc-400">
            <div>Progress</div>
            <div><span id="pct">0%</span></div>
          </div>

          <div class="mt-2">
            <div class="h-2 w-full overflow-hidden rounded-full bg-zinc-800">
              <div id="bar" class="h-2 w-0 rounded-full bg-zinc-200 transition-[width]"></div>
            </div>
          </div>

          <div class="mt-3 grid grid-cols-3 gap-3 text-xs">
            <div class="rounded-lg border border-zinc-800 bg-zinc-950/30 p-2">
              <div class="text-zinc-500">Speed</div>
              <div class="mt-1 font-mono text-zinc-200" id="spd">0 B/s</div>
            </div>
            <div class="rounded-lg border border-zinc-800 bg-zinc-950/30 p-2">
              <div class="text-zinc-500">Uploaded</div>
              <div class="mt-1 font-mono text-zinc-200"><span id="up">0 B</span></div>
            </div>
            <div class="rounded-lg border border-zinc-800 bg-zinc-950/30 p-2">
              <div class="text-zinc-500">Total</div>
              <div class="mt-1 font-mono text-zinc-200"><span id="tot">0 B</span></div>
            </div>
          </div>
        </div>

        <div id="statusWrap" class="hidden rounded-xl border border-zinc-800 bg-zinc-950/30 p-4">
          <div class="text-xs font-medium text-zinc-400">Status</div>
          <pre id="status" class="mt-2 whitespace-pre-wrap break-words text-xs font-mono text-zinc-200"></pre>
        </div>
      </div>
    </div>
  </div>

<script>
(function () {
  const MAX_BYTES = {{.MaxBytes}};

  const btn = document.getElementById('btn');
  const fileInput = document.getElementById('file');
  const passInput = document.getElementById('password');

  const pct = document.getElementById('pct');
  const spd = document.getElementById('spd');
  const up = document.getElementById('up');
  const tot = document.getElementById('tot');

  const bar = document.getElementById('bar');
  const phase = document.getElementById('phase');

  const statusWrap = document.getElementById('statusWrap');
  const status = document.getElementById('status');

  const fileName = document.getElementById('fileName');
  const fileSize = document.getElementById('fileSize');

  function humanBytes(n) {
    const units = ["B","KB","MB","GB","TB"];
    let i = 0;
    let v = n;
    while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
    return (i === 0 ? v.toFixed(0) : v.toFixed(2)) + " " + units[i];
  }

  function setDisabled(disabled) {
    btn.disabled = disabled;
    fileInput.disabled = disabled;
    passInput.disabled = disabled;
  }

  function setStatus(text) {
    statusWrap.classList.remove('hidden');
    status.textContent = text;
  }

  function clearStatus() {
    statusWrap.classList.add('hidden');
    status.textContent = "";
  }

  function setProgress(percent) {
    const p = Math.max(0, Math.min(100, percent));
    pct.textContent = p + "%";
    bar.style.width = p + "%";
  }

  fileInput.addEventListener('change', () => {
    clearStatus();

    const f = fileInput.files && fileInput.files[0];
    if (!f) {
      fileName.textContent = "none";
      fileSize.textContent = "0 B";
      tot.textContent = "0 B";
      setProgress(0);
      spd.textContent = "0 B/s";
      up.textContent = "0 B";
      phase.textContent = "Idle";
      return;
    }

    fileName.textContent = f.name;
    fileSize.textContent = humanBytes(f.size);
    tot.textContent = humanBytes(f.size);

    if (f.size > MAX_BYTES) {
      setStatus(
        "File too large.\n" +
        "Max: " + humanBytes(MAX_BYTES) + "\n" +
        "Selected: " + humanBytes(f.size)
      );
      setProgress(0);
      spd.textContent = "0 B/s";
      up.textContent = "0 B";
      phase.textContent = "Idle";
    }
  });

  btn.addEventListener('click', () => {
    clearStatus();

    const f = fileInput.files && fileInput.files[0];
    const p = passInput.value || "";

    if (!f) { setStatus("Pick a file."); return; }
    if (!p) { setStatus("Enter the password."); return; }

    if (f.size > MAX_BYTES) {
      setStatus(
        "File too large.\n" +
        "Max: " + humanBytes(MAX_BYTES) + "\n" +
        "Selected: " + humanBytes(f.size)
      );
      return;
    }

    setDisabled(true);

    setProgress(0);
    spd.textContent = "0 B/s";
    up.textContent = "0 B";
    tot.textContent = humanBytes(f.size);

    phase.textContent = "Sending";

    const form = new FormData();
    form.append("file", f, f.name);

    const xhr = new XMLHttpRequest();
    xhr.open("POST", "/upload", true);

    // Password in header so server can reject early without reading the body.
    xhr.setRequestHeader("X-Upload-Password", p);

    const tStart = performance.now();
    let tLast = tStart;
    let lastLoaded = 0;

    let waitTimer = null;
    let waiting = false;

    function startWaiting() {
      if (waiting) return;
      waiting = true;
      phase.textContent = "Waiting for server";
      waitTimer = setInterval(() => {
        const now = performance.now();
        const secs = (now - tStart) / 1000;
        const avgBps = f.size / Math.max(secs, 0.001);
        spd.textContent = humanBytes(avgBps) + "/s avg";
      }, 200);
    }

    xhr.upload.onprogress = (e) => {
      if (!e.lengthComputable) return;

      const now = performance.now();
      const dt = (now - tLast) / 1000;
      const dBytes = e.loaded - lastLoaded;

      if (dt > 0.2) {
        const bps = dBytes / dt;
        spd.textContent = humanBytes(bps) + "/s";
        tLast = now;
        lastLoaded = e.loaded;
      }

      const percent = Math.floor((e.loaded / e.total) * 100);
      setProgress(percent);
      up.textContent = humanBytes(e.loaded);
      tot.textContent = humanBytes(e.total);

      if (percent >= 100) startWaiting();
    };

    xhr.upload.onload = () => {
      setProgress(100);
      up.textContent = humanBytes(f.size);
      startWaiting();
    };

    xhr.onload = () => {
      if (waitTimer) clearInterval(waitTimer);

      const tEnd = performance.now();
      const secs = (tEnd - tStart) / 1000;
      const avgBps = f.size / Math.max(secs, 0.001);

      if (xhr.status >= 200 && xhr.status < 300) {
        phase.textContent = "Done";
        setStatus(
          xhr.responseText.trim() +
          "\nend_to_end=" + secs.toFixed(2) + "s" +
          "\navg_speed=" + humanBytes(avgBps) + "/s"
        );
      } else {
        phase.textContent = "Error";
        setStatus(
          "error " + xhr.status + "\n" + (xhr.responseText || "") +
          "\nend_to_end=" + secs.toFixed(2) + "s"
        );
      }

      setDisabled(false);
    };

    xhr.onerror = () => {
      if (waitTimer) clearInterval(waitTimer);
      phase.textContent = "Error";
      setStatus("network error");
      setDisabled(false);
    };

    xhr.send(form);
  });
})();
</script>
</body>
</html>
`))
