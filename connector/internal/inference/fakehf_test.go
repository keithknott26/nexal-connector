package inference

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testRepo = "mlx-community/Qwen3-4B-4bit"
	testRev  = "0123456789abcdef0123456789abcdef01234567"
	testID   = "qwen3-4b-q4"
)

func shaHex(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

type seenReq struct {
	Path   string
	Range  string
	Header http.Header
}

// fakeHF is an httptest TLS server that speaks just enough of the Hub.
type fakeHF struct {
	t     *testing.T
	srv   *httptest.Server
	mu    sync.Mutex
	seen  []seenReq
	files map[string][]byte

	infoSHA     string
	license     string
	gated       bool
	treeExtra   []map[string]any
	lfsOverride map[string]string // name -> bogus lfs oid
	// fileHook may take over a file response; return true when handled.
	fileHook func(w http.ResponseWriter, r *http.Request, name string, data []byte) bool
}

func goodConfig() map[string]any {
	return map[string]any{
		"model_type": "qwen3", "architectures": []any{"Qwen3ForCausalLM"},
		"hidden_size": 2560, "num_hidden_layers": 36, "intermediate_size": 9728, "vocab_size": 151936,
		"num_attention_heads": 32, "num_key_value_heads": 8, "head_dim": 128, "tie_word_embeddings": true,
		"max_position_embeddings": 40960, "rms_norm_eps": 1e-6, "rope_theta": 1000000,
	}
}

func jsonBytes(v any) []byte { b, _ := json.Marshal(v); return b }

func defaultFiles() map[string][]byte {
	return map[string][]byte{
		"config.json":           jsonBytes(goodConfig()),
		"tokenizer.json":        bytes.Repeat([]byte("T"), 5000),
		"tokenizer_config.json": jsonBytes(map[string]any{"tokenizer_class": "Qwen2Tokenizer"}),
		"model.safetensors":     bytes.Repeat([]byte("W0123456789"), 40000), // 440000 bytes
	}
}

func newFakeHF(t *testing.T) *fakeHF {
	f := &fakeHF{t: t, files: defaultFiles(), infoSHA: testRev, license: "apache-2.0", lfsOverride: map[string]string{}}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeHF) host() string { u, _ := url.Parse(f.srv.URL); return u.Host }

func (f *fakeHF) client() *HFClient {
	c, err := NewHFClient(HFOptions{BaseURL: f.srv.URL, HTTPClient: f.srv.Client(),
		AllowHost: func(hp string) bool { return hp == f.host() }})
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}

func (f *fakeHF) requests() []seenReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]seenReq(nil), f.seen...)
}

func (f *fakeHF) fileRequests() int {
	n := 0
	for _, r := range f.requests() {
		if strings.Contains(r.Path, "/resolve/") {
			n++
		}
	}
	return n
}

func (f *fakeHF) setFile(name string, data []byte) { f.mu.Lock(); f.files[name] = data; f.mu.Unlock() }

func (f *fakeHF) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.seen = append(f.seen, seenReq{Path: r.URL.Path, Range: r.Header.Get("Range"), Header: r.Header.Clone()})
	f.mu.Unlock()
	api := "/api/models/" + testRepo
	switch {
	case r.URL.Path == api+"/revision/"+testRev:
		card := map[string]any{}
		if f.license != "" {
			card["license"] = f.license
		}
		doc := map[string]any{"sha": f.infoSHA, "cardData": card, "tags": []string{}}
		if f.gated {
			doc["gated"] = "manual"
		}
		json.NewEncoder(w).Encode(doc)
	case r.URL.Path == api+"/tree/"+testRev:
		var out []map[string]any
		f.mu.Lock()
		for name, data := range f.files {
			e := map[string]any{"type": "file", "path": name, "size": len(data), "oid": "deadbeef"}
			if strings.HasSuffix(name, ".safetensors") {
				oid := shaHex(data)
				if bad, ok := f.lfsOverride[name]; ok {
					oid = bad
				}
				e["lfs"] = map[string]any{"oid": oid, "size": len(data)}
			}
			out = append(out, e)
		}
		f.mu.Unlock()
		out = append(out, f.treeExtra...)
		json.NewEncoder(w).Encode(out)
	case strings.HasPrefix(r.URL.Path, "/"+testRepo+"/resolve/"+testRev+"/"):
		name := strings.TrimPrefix(r.URL.Path, "/"+testRepo+"/resolve/"+testRev+"/")
		f.mu.Lock()
		data, ok := f.files[name]
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		if f.fileHook != nil && f.fileHook(w, r, name, data) {
			return
		}
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
	case strings.HasPrefix(r.URL.Path, "/cdn/"):
		name := strings.TrimPrefix(r.URL.Path, "/cdn/")
		f.mu.Lock()
		data := f.files[name]
		f.mu.Unlock()
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
	default:
		http.NotFound(w, r)
	}
}

// pinFor returns correct pins for the fake's current files.
func (f *fakeHF) pinFiles() []PinFile {
	var out []PinFile
	f.mu.Lock()
	defer f.mu.Unlock()
	for name, d := range f.files {
		out = append(out, PinFile{Name: name, Size: int64(len(d)), SHA256: shaHex(d)})
	}
	sortPinFiles(out)
	return out
}

func sortPinFiles(p []PinFile) {
	for i := 1; i < len(p); i++ {
		for j := i; j > 0 && p[j].Name < p[j-1].Name; j-- {
			p[j], p[j-1] = p[j-1], p[j]
		}
	}
}

func baseCatalog(t *testing.T) CatalogFile {
	t.Helper()
	c, err := LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// pinnedCatalog pins testID to the fake's files without going through the pin command.
func (f *fakeHF) pinnedCatalog(t *testing.T) CatalogFile {
	t.Helper()
	arch := ArchSpec{Layers: 36, KVHeads: 8, HeadDim: 128, HiddenSize: 2560, IntermediateSize: 9728, VocabSize: 151936}
	c, err := MergePins(baseCatalog(t), &PinsFile{SchemaVersion: 1, Pins: map[string]Pin{
		testID: {HFRepo: testRepo, Revision: testRev, License: "apache-2.0", ModelType: "qwen3", Arch: &arch, Files: f.pinFiles()}}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
