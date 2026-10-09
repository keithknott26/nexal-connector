package inference

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("error = %v, want code %q", err, code)
	}
}

func pinOf(f *fakeHF, name string) PinFile {
	d := f.files[name]
	return PinFile{Name: name, Size: int64(len(d)), SHA256: shaHex(d)}
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var n []string
	for _, e := range ents {
		n = append(n, e.Name())
	}
	return n
}

func TestHFHostAllowed(t *testing.T) {
	for host, want := range map[string]bool{
		"huggingface.co": true, "HuggingFace.co": true, "huggingface.co:443": true, "cdn-lfs.hf.co": true, "cdn-lfs.huggingface.co": true, "x.y.huggingface.co": true, ".huggingface.co": false, "evilhuggingface.co": false, "cas-bridge.xethub.hf.co": true,
		"hf.co": false, ".hf.co": false, "evilhf.co": false, "huggingface.co.evil.com": false, "evil.com": false,
		"huggingface.co:8443": false, "127.0.0.1": false, "localhost": false, "x.hf.co.evil.com": false, "": false,
		"huggingface.co.": false, "a..hf.co": false,
	} {
		if got := HFHostAllowed(host); got != want {
			t.Errorf("HFHostAllowed(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestDownloadSuccess(t *testing.T) {
	f := newFakeHF(t)
	dir := t.TempDir()
	c := f.client()
	var last int64
	pf := pinOf(f, "model.safetensors")
	if err := c.DownloadFile(context.Background(), testRepo, testRev, pf, dir, func(n int64) { last = n }); err != nil {
		t.Fatal(err)
	}
	if last != pf.Size {
		t.Errorf("progress ended at %d", last)
	}
	if got := dirNames(t, dir); len(got) != 1 || got[0] != "model.safetensors" {
		t.Fatalf("dir = %v", got)
	}
	st, _ := os.Stat(filepath.Join(dir, "model.safetensors"))
	if st.Mode().Perm() != 0o400 {
		t.Errorf("mode = %v", st.Mode().Perm())
	}
	for _, r := range f.requests() {
		for _, h := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
			if r.Header.Get(h) != "" {
				t.Errorf("credential header %s sent", h)
			}
		}
		if r.Header.Get("Accept-Encoding") != "identity" {
			t.Errorf("Accept-Encoding = %q", r.Header.Get("Accept-Encoding"))
		}
	}
	// A finished file is reused without any request.
	before := f.fileRequests()
	if err := c.DownloadFile(context.Background(), testRepo, testRev, pf, dir, nil); err != nil || f.fileRequests() != before {
		t.Errorf("finished file refetched (%v)", err)
	}
}

func TestDownloadHashMismatchLeavesNothing(t *testing.T) {
	f := newFakeHF(t)
	dir := t.TempDir()
	pf := pinOf(f, "model.safetensors")
	pf.SHA256 = strings.Repeat("0", 64)
	err := f.client().DownloadFile(context.Background(), testRepo, testRev, pf, dir, nil)
	wantCode(t, err, "hash_mismatch")
	if got := dirNames(t, dir); len(got) != 0 {
		t.Fatalf("files left behind: %v", got)
	}
}

func TestDownloadSameSizeDifferentBytes(t *testing.T) {
	f := newFakeHF(t)
	dir := t.TempDir()
	pf := pinOf(f, "config.json")
	tampered := []byte(strings.Replace(string(f.files["config.json"]), "qwen3", "qwen4", 1))
	f.setFile("config.json", tampered)
	wantCode(t, f.client().DownloadFile(context.Background(), testRepo, testRev, pf, dir, nil), "hash_mismatch")
	if got := dirNames(t, dir); len(got) != 0 {
		t.Fatalf("files left: %v", got)
	}
}

func TestDownloadSizeOverflow(t *testing.T) {
	t.Run("declared content-length too large", func(t *testing.T) {
		f := newFakeHF(t)
		dir := t.TempDir()
		pf := pinOf(f, "tokenizer.json")
		f.setFile("tokenizer.json", append(f.files["tokenizer.json"], 'X'))
		wantCode(t, f.client().DownloadFile(context.Background(), testRepo, testRev, pf, dir, nil), "size_overflow")
		if got := dirNames(t, dir); len(got) != 0 {
			t.Fatalf("left: %v", got)
		}
	})
	t.Run("streamed body exceeds the pin", func(t *testing.T) {
		f := newFakeHF(t)
		dir := t.TempDir()
		pf := pinOf(f, "tokenizer.json")
		f.fileHook = func(w http.ResponseWriter, r *http.Request, name string, data []byte) bool {
			w.WriteHeader(200) // chunked: no Content-Length
			w.Write(data)
			w.(http.Flusher).Flush()
			w.Write([]byte("EXTRA BYTES BEYOND THE PIN"))
			return true
		}
		wantCode(t, f.client().DownloadFile(context.Background(), testRepo, testRev, pf, dir, nil), "size_overflow")
		if got := dirNames(t, dir); len(got) != 0 {
			t.Fatalf("left: %v", got)
		}
	})
}

func TestDownloadRedirects(t *testing.T) {
	t.Run("redirect within the allowed host is followed", func(t *testing.T) {
		f := newFakeHF(t)
		dir := t.TempDir()
		f.fileHook = func(w http.ResponseWriter, r *http.Request, name string, data []byte) bool {
			http.Redirect(w, r, "/cdn/"+name, http.StatusFound)
			return true
		}
		if err := f.client().DownloadFile(context.Background(), testRepo, testRev, pinOf(f, "config.json"), dir, nil); err != nil {
			t.Fatal(err)
		}
		for _, r := range f.requests() {
			if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
				t.Error("credentials on a redirect hop")
			}
		}
	})
	t.Run("redirect to a disallowed host is refused", func(t *testing.T) {
		f := newFakeHF(t)
		dir := t.TempDir()
		f.fileHook = func(w http.ResponseWriter, r *http.Request, name string, data []byte) bool {
			http.Redirect(w, r, "https://evil.example.com/steal/"+name, http.StatusFound)
			return true
		}
		err := f.client().DownloadFile(context.Background(), testRepo, testRev, pinOf(f, "config.json"), dir, nil)
		wantCode(t, err, "redirect_refused")
		if got := dirNames(t, dir); len(got) > 1 || (len(got) == 1 && got[0] != "config.json.part") {
			t.Fatalf("unexpected files %v", got)
		}
		if _, err := os.Stat(filepath.Join(dir, "config.json")); err == nil {
			t.Error("final file exists")
		}
	})
	t.Run("lookalike hosts are refused by the production rule", func(t *testing.T) {
		f := newFakeHF(t)
		c, err := NewHFClient(HFOptions{BaseURL: "https://huggingface.co", HTTPClient: f.srv.Client()})
		if err != nil {
			t.Fatal(err)
		}
		f.fileHook = func(w http.ResponseWriter, r *http.Request, name string, data []byte) bool { return false }
		// Point the production client at the fake by rewriting nothing: any request to
		// the fake's 127.0.0.1 host must be refused before dialing.
		u := f.srv.URL + "/x"
		req, _ := http.NewRequest("GET", u, nil)
		if _, err := c.get(context.Background(), req.URL, nil); err == nil {
			t.Error("production client reached a non-Hugging-Face host")
		}
	})
	t.Run("redirect to http is refused", func(t *testing.T) {
		plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("x")) }))
		defer plain.Close()
		f := newFakeHF(t)
		f.fileHook = func(w http.ResponseWriter, r *http.Request, name string, data []byte) bool {
			http.Redirect(w, r, plain.URL+"/f", http.StatusFound)
			return true
		}
		c, err := NewHFClient(HFOptions{BaseURL: f.srv.URL, HTTPClient: f.srv.Client(), AllowHost: func(string) bool { return true }})
		if err != nil {
			t.Fatal(err)
		}
		wantCode(t, c.DownloadFile(context.Background(), testRepo, testRev, pinOf(f, "config.json"), t.TempDir(), nil), "redirect_refused")
	})
	t.Run("too many redirects", func(t *testing.T) {
		f := newFakeHF(t)
		f.fileHook = func(w http.ResponseWriter, r *http.Request, name string, data []byte) bool {
			http.Redirect(w, r, r.URL.Path, http.StatusFound)
			return true
		}
		wantCode(t, f.client().DownloadFile(context.Background(), testRepo, testRev, pinOf(f, "config.json"), t.TempDir(), nil), "redirect_refused")
	})
}

func TestHTTPSchemeRefused(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("plain http server was contacted") }))
	defer plain.Close()
	_, err := NewHFClient(HFOptions{BaseURL: plain.URL, AllowHost: func(string) bool { return true }})
	wantCode(t, err, "insecure_url")
	if _, err := NewHFClient(HFOptions{BaseURL: "https://user:pw@huggingface.co"}); err == nil {
		t.Error("URL credentials accepted")
	}
	if _, err := NewHFClient(HFOptions{BaseURL: "https://evil.example.com"}); err == nil {
		t.Error("foreign base host accepted")
	}
	if _, err := NewHFClient(HFOptions{}); err != nil {
		t.Errorf("default client: %v", err)
	}
}

func TestDownloadBadNamesAndRevisions(t *testing.T) {
	f := newFakeHF(t)
	c := f.client()
	dir := t.TempDir()
	good := strings.Repeat("a", 64)
	for _, name := range []string{"../model.safetensors", "../../etc/passwd", "a/model.safetensors", "/etc/passwd", `..\model.safetensors`,
		"model.safetensors/..", "modeling_qwen3.py", "pytorch_model.bin", "config.json\x00.py", "", "..", ".", "model..safetensors", "%2e%2e/config.json", "subdir/config.json"} {
		err := c.DownloadFile(context.Background(), testRepo, testRev, PinFile{Name: name, Size: 5, SHA256: good}, dir, nil)
		wantCode(t, err, "bad_file_name")
	}
	if f.fileRequests() != 0 {
		t.Error("a request was made for a refused name")
	}
	for _, rev := range []string{"main", "refs/pr/1", "v1.0", testRev[:39], ""} {
		err := c.DownloadFile(context.Background(), testRepo, rev, pinOf(f, "config.json"), dir, nil)
		wantCode(t, err, "mutable_revision")
	}
	for _, repo := range []string{"../x/y", "a", "a/b/c", "a/../b", "a b/c"} {
		if err := c.DownloadFile(context.Background(), repo, testRev, pinOf(f, "config.json"), dir, nil); err == nil {
			t.Errorf("repo %q accepted", repo)
		}
	}
	if err := c.DownloadFile(context.Background(), testRepo, testRev, PinFile{Name: "config.json", Size: 0, SHA256: good}, dir, nil); err == nil {
		t.Error("unpinned size accepted")
	}
	if got := dirNames(t, dir); len(got) != 0 {
		t.Errorf("files created: %v", got)
	}
	// Nothing escaped above the directory either.
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "model.safetensors")); err == nil {
		t.Error("file written outside the directory")
	}
}

func TestDownloadResume(t *testing.T) {
	data := defaultFiles()["model.safetensors"]
	t.Run("truncated stream keeps the part and the next call resumes with Range", func(t *testing.T) {
		f := newFakeHF(t)
		dir := t.TempDir()
		pf := pinOf(f, "model.safetensors")
		var calls int32
		f.fileHook = func(w http.ResponseWriter, r *http.Request, name string, d []byte) bool {
			if atomic.AddInt32(&calls, 1) == 1 {
				w.Header().Set("Content-Length", "440000")
				w.WriteHeader(200)
				w.Write(d[:100000])
				w.(http.Flusher).Flush()
				panic(http.ErrAbortHandler) // drop the connection mid-body
			}
			return false
		}
		err := f.client().DownloadFile(context.Background(), testRepo, testRev, pf, dir, nil)
		if err == nil {
			t.Fatal("truncated download succeeded")
		}
		part := filepath.Join(dir, "model.safetensors.part")
		st, serr := os.Stat(part)
		if serr != nil || st.Size() != 100000 {
			t.Fatalf("part = %v %v (err %v)", st, serr, err)
		}
		if _, e := os.Stat(filepath.Join(dir, "model.safetensors")); e == nil {
			t.Fatal("final file exists after a failed download")
		}
		var first int64 = -1
		if err := f.client().DownloadFile(context.Background(), testRepo, testRev, pf, dir, func(n int64) {
			if first < 0 {
				first = n
			}
		}); err != nil {
			t.Fatal(err)
		}
		if first < 100000 {
			t.Errorf("progress did not start from the resumed offset: %d", first)
		}
		reqs := f.requests()
		if last := reqs[len(reqs)-1]; last.Range != "bytes=100000-" {
			t.Errorf("Range = %q", last.Range)
		}
		got, _ := os.ReadFile(filepath.Join(dir, "model.safetensors"))
		if string(got) != string(data) {
			t.Error("resumed file differs")
		}
		if names := dirNames(t, dir); len(names) != 1 {
			t.Errorf("dir = %v", names)
		}
	})
	t.Run("server ignores Range: restarts from zero", func(t *testing.T) {
		f := newFakeHF(t)
		dir := t.TempDir()
		pf := pinOf(f, "model.safetensors")
		os.WriteFile(filepath.Join(dir, "model.safetensors.part"), data[:50000], 0o600)
		f.fileHook = func(w http.ResponseWriter, r *http.Request, name string, d []byte) bool {
			w.Write(d) // always 200 with the full body
			return true
		}
		if err := f.client().DownloadFile(context.Background(), testRepo, testRev, pf, dir, nil); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(filepath.Join(dir, "model.safetensors"))
		if string(got) != string(data) {
			t.Error("file differs")
		}
	})
	t.Run("corrupt prefix is caught by the hash and deleted", func(t *testing.T) {
		f := newFakeHF(t)
		dir := t.TempDir()
		pf := pinOf(f, "model.safetensors")
		bad := append([]byte("EVIL"), data[4:50000]...)
		os.WriteFile(filepath.Join(dir, "model.safetensors.part"), bad, 0o600)
		wantCode(t, f.client().DownloadFile(context.Background(), testRepo, testRev, pf, dir, nil), "hash_mismatch")
		if got := dirNames(t, dir); len(got) != 0 {
			t.Fatalf("left: %v", got)
		}
	})
	t.Run("oversized part is discarded", func(t *testing.T) {
		f := newFakeHF(t)
		dir := t.TempDir()
		pf := pinOf(f, "tokenizer.json")
		os.WriteFile(filepath.Join(dir, "tokenizer.json.part"), make([]byte, 99999), 0o600)
		if err := f.client().DownloadFile(context.Background(), testRepo, testRev, pf, dir, nil); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("complete part is verified without a request", func(t *testing.T) {
		f := newFakeHF(t)
		dir := t.TempDir()
		pf := pinOf(f, "tokenizer.json")
		os.WriteFile(filepath.Join(dir, "tokenizer.json.part"), f.files["tokenizer.json"], 0o600)
		if err := f.client().DownloadFile(context.Background(), testRepo, testRev, pf, dir, nil); err != nil || f.fileRequests() != 0 {
			t.Fatalf("err=%v requests=%d", err, f.fileRequests())
		}
	})
	t.Run("inconsistent Content-Range is refused", func(t *testing.T) {
		f := newFakeHF(t)
		dir := t.TempDir()
		pf := pinOf(f, "tokenizer.json")
		os.WriteFile(filepath.Join(dir, "tokenizer.json.part"), f.files["tokenizer.json"][:1000], 0o600)
		f.fileHook = func(w http.ResponseWriter, r *http.Request, name string, d []byte) bool {
			w.Header().Set("Content-Range", "bytes 0-4999/5000")
			w.WriteHeader(206)
			w.Write(d)
			return true
		}
		wantCode(t, f.client().DownloadFile(context.Background(), testRepo, testRev, pf, dir, nil), "bad_range")
		if _, err := os.Stat(filepath.Join(dir, "tokenizer.json")); err == nil {
			t.Error("final exists")
		}
	})
}

func TestDownloadCancel(t *testing.T) {
	f := newFakeHF(t)
	dir := t.TempDir()
	pf := pinOf(f, "model.safetensors")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.fileHook = func(w http.ResponseWriter, r *http.Request, name string, d []byte) bool {
		w.Header().Set("Content-Length", "440000")
		w.WriteHeader(200)
		w.Write(d[:80000])
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		return true
	}
	done := make(chan error, 1)
	go func() {
		done <- f.client().DownloadFile(ctx, testRepo, testRev, pf, dir, func(n int64) {
			if n >= 80000 {
				cancel()
			}
		})
	}()
	select {
	case err := <-done:
		wantCode(t, err, "cancelled")
	case <-time.After(10 * time.Second):
		t.Fatal("cancel did not stop the download")
	}
	if _, err := os.Stat(filepath.Join(dir, "model.safetensors")); err == nil {
		t.Error("final file exists after cancel")
	}
	if st, err := os.Stat(filepath.Join(dir, "model.safetensors.part")); err != nil || st.Size() == 0 {
		t.Errorf("part not kept for resuming: %v", err)
	}
	// Cancelled before starting: no request, cancelled code.
	pre, c2 := context.WithCancel(context.Background())
	c2()
	before := len(f.requests())
	wantCode(t, f.client().DownloadFile(pre, testRepo, testRev, pinOf(f, "config.json"), t.TempDir(), nil), "cancelled")
	if len(f.requests()) != before {
		t.Error("a request was made on a cancelled context")
	}
}

func TestDownloadStatusErrors(t *testing.T) {
	f := newFakeHF(t)
	f.fileHook = func(w http.ResponseWriter, r *http.Request, name string, d []byte) bool {
		http.Error(w, "nope", http.StatusForbidden)
		return true
	}
	wantCode(t, f.client().DownloadFile(context.Background(), testRepo, testRev, pinOf(f, "config.json"), t.TempDir(), nil), "repo_not_public")
	f2 := newFakeHF(t)
	f2.fileHook = func(w http.ResponseWriter, r *http.Request, name string, d []byte) bool {
		http.Error(w, "boom", 500)
		return true
	}
	wantCode(t, f2.client().DownloadFile(context.Background(), testRepo, testRev, pinOf(f2, "config.json"), t.TempDir(), nil), "http_status")
	dir := t.TempDir()
	f3 := newFakeHF(t)
	pf := pinOf(f3, "config.json")
	delete(f3.files, "config.json")
	wantCode(t, f3.client().DownloadFile(context.Background(), testRepo, testRev, pf, dir, nil), "file_not_found")
}
