# Upload

A small Go HTTP server for uploading a single file using a shared password.
Uploads stream directly to disk, with a configurable file size limit.

Enter the development environment with `nix-shell`. The shell uses your
configured `nixpkgs` and provides Go, `gopls`, and `goimports` (from `gotools`).
Go 1.22 or newer also works without Nix. The project has no external Go dependencies.

```sh
go run . -password 'your-shared-password' -listen 127.0.0.1:8080
```

Open http://127.0.0.1:8080 to upload a file. By default, files are saved in
`./uploads` and the maximum file size is 200 MiB. Use `-dir` and `-max` to change
these settings; size suffixes such as `K`, `MB`, and `GB` use powers of 1024.
The server allows another 10 MiB of multipart overhead. It accepts the first
multipart part named `file` and ignores subsequent parts.

The API accepts `POST /upload` with a `multipart/form-data` body and an
`X-Upload-Password` header. Successful responses include `saved_as` and `bytes`.

```sh
go test ./...
go vet ./...
go build -o uploader .
```

Run and build the whole package (`.`) so Go includes all source files. The page
in `templates/index.html` is embedded in the binary at build time. The browser
loads Tailwind CSS from a CDN.

`main.go` handles flags and startup, `server.go` configures HTTP routing and
shutdown, `index.go` renders the page, `upload.go` streams uploads, and
`size.go` and `filename.go` hold the size and filename helpers. SIGINT and
SIGTERM stop the listener and allow active requests up to 10 seconds to finish.
