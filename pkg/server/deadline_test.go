package server

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

// rawRequest sends head and the start of a body over a real connection, then
// stops sending and returns the response and how long it took to arrive.
func rawRequest(t *testing.T, address, head string, body []byte) (*http.Response, time.Duration) {
	t.Helper()
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	start := time.Now()
	if _, err := io.WriteString(conn, head); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(body); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("no response to a stalled body: %v", err)
	}
	return response, time.Since(start)
}

func basicHeader() string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("Test User:"+testPassword))
}

func multipartUpload(t *testing.T, name string, contents []byte) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	part.Write(contents)
	mw.Close()
	return body.Bytes(), mw.FormDataContentType()
}

func TestSlowRequestBodiesAreCutOff(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 1<<20)
	srv.formTimeout = 300 * time.Millisecond
	srv.uploadTimeout = 500 * time.Millisecond
	server := httptest.NewServer(srv.Handler())
	defer server.Close()
	address := server.Listener.Addr().String()

	// A sign-in form that never finishes arriving is answered after the form
	// timeout instead of holding the connection open.
	login, elapsed := rawRequest(t, address, "POST /login HTTP/1.1\r\nHost: "+address+
		"\r\nContent-Type: application/x-www-form-urlencoded\r\nContent-Length: 100\r\n\r\n", []byte("username=Test"))
	if login.StatusCode != http.StatusRequestTimeout || elapsed > 3*time.Second {
		t.Fatalf("stalled sign-in form: %d after %v", login.StatusCode, elapsed)
	}

	// A stalled upload fails after the idle timeout and releases the account:
	// the next upload and file listing proceed, and no staging folder remains.
	body, contentType := multipartUpload(t, "stalled.bin", bytes.Repeat([]byte("x"), 1000))
	upload, elapsed := rawRequest(t, address, fmt.Sprintf("POST /upload HTTP/1.1\r\nHost: %s\r\nAuthorization: %s\r\n"+
		"Content-Type: %s\r\nContent-Length: %d\r\n\r\n", address, basicHeader(), contentType, len(body)), body[:len(body)/2])
	if upload.StatusCode != http.StatusRequestTimeout || elapsed > 3*time.Second {
		t.Fatalf("stalled upload: %d after %v", upload.StatusCode, elapsed)
	}
	next, contentType := multipartUpload(t, "next.txt", []byte("next"))
	r, _ := http.NewRequest(http.MethodPost, server.URL+"/upload", bytes.NewReader(next))
	r.Header.Set("Authorization", basicHeader())
	r.Header.Set("Content-Type", contentType)
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Do(r)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("upload after a stalled upload: %v, %v", response, err)
	}
	response.Body.Close()
	if staging := stagingFolders(t, srv); len(staging) != 0 {
		t.Fatalf("stalled upload left staging folders: %v", staging)
	}

	// An upload that keeps sending data may take longer than the idle timeout.
	body, contentType = multipartUpload(t, "slow.bin", bytes.Repeat([]byte("y"), 2000))
	reader, writer := io.Pipe()
	go func() {
		for chunk := range slices.Chunk(body, len(body)/10+1) {
			time.Sleep(150 * time.Millisecond)
			writer.Write(chunk)
		}
		writer.Close()
	}()
	r, _ = http.NewRequest(http.MethodPost, server.URL+"/upload", reader)
	r.ContentLength = int64(len(body))
	r.Header.Set("Authorization", basicHeader())
	r.Header.Set("Content-Type", contentType)
	start := time.Now()
	response, err = (&http.Client{Timeout: 10 * time.Second}).Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if text, _ := io.ReadAll(response.Body); response.StatusCode != http.StatusOK || time.Since(start) < srv.uploadTimeout {
		t.Fatalf("slow but active upload: %d after %v: %s", response.StatusCode, time.Since(start), strings.TrimSpace(string(text)))
	}
}
