package inference

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// This file is the ONLY place in the package that opens network connections,
// and it is used only by `nexal inference pin` and `install`, never by
// preflight. Rules enforced for every request, including every redirect hop:
//
//   - https only, no URL credentials, no non-443 port;
//   - the host must be huggingface.co or a subdomain of hf.co;
//   - no Authorization, Cookie or other credential is ever attached (the client
//     has no cookie jar), so gated or private repositories are simply refused;
//   - at most maxRedirects hops;
//   - response bodies are read through hard size caps.

const (
	defaultHFBase = "https://huggingface.co"
	maxRedirects  = 5
	maxAPIBytes   = 8 << 20
	// MaxJSONFileBytes mirrors the runtime's MAX_JSON for small metadata files.
	MaxJSONFileBytes = 32 << 20
)

// HFOptions configure NewHFClient. The zero value is the production client;
// tests inject a base URL, an HTTP client trusting a test certificate and a
// host predicate for the httptest server.
type HFOptions struct {
	BaseURL    string
	HTTPClient *http.Client
	// AllowHost receives a URL's host[:port] and reports whether requests (and
	// redirects) to it are permitted. nil means huggingface.co / *.hf.co on 443.
	AllowHost func(hostport string) bool
}

// HFClient talks to the Hugging Face Hub with the restrictions above.
type HFClient struct {
	base  *url.URL
	http  *http.Client
	allow func(string) bool
}

// HFHostAllowed is the production host rule: huggingface.co or *.hf.co, https
// default port only.
func HFHostAllowed(hostport string) bool {
	host, port := hostport, ""
	if h, p, err := net.SplitHostPort(hostport); err == nil {
		host, port = h, p
	}
	if port != "" && port != "443" {
		return false
	}
	host = strings.ToLower(host)
	if host == "huggingface.co" {
		return true
	}
	// The Hub serves LFS/xet content from subdomains (cdn-lfs.huggingface.co,
	// cas-bridge.xethub.hf.co), so both suffixes are allowed; https only.
	if strings.HasPrefix(host, ".") || strings.Contains(host, "..") {
		return false
	}
	return (strings.HasSuffix(host, ".hf.co") && len(host) > len(".hf.co")) ||
		(strings.HasSuffix(host, ".huggingface.co") && len(host) > len(".huggingface.co"))
}

// NewHFClient builds a client. It fails if the base URL is not an allowed
// https URL.
func NewHFClient(o HFOptions) (*HFClient, error) {
	c := &HFClient{allow: o.AllowHost}
	if c.allow == nil {
		c.allow = HFHostAllowed
	}
	raw := o.BaseURL
	if raw == "" {
		raw = defaultHFBase
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errf("insecure_url", "", "invalid base URL")
	}
	c.base = u
	if err := c.checkURL(u); err != nil {
		return nil, err
	}
	hc := &http.Client{}
	if o.HTTPClient != nil {
		cp := *o.HTTPClient
		hc = &cp
	}
	hc.Jar = nil // never carry cookies
	hc.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return errf("redirect_refused", "", "too many redirects")
		}
		if err := c.checkURL(req.URL); err != nil {
			return errf("redirect_refused", "report this if Hugging Face changed its download hosts", "refusing redirect to %s: %s", req.URL.Host, err.Message)
		}
		req.Header.Del("Authorization")
		req.Header.Del("Cookie")
		return nil
	}
	c.http = hc
	return c, nil
}

var contentRange = regexp.MustCompile(`^bytes (\d{1,18})-(\d{1,18})/(\d{1,18})$`)

func (c *HFClient) checkURL(u *url.URL) *Error {
	if u.Scheme != "https" {
		return errf("insecure_url", "only https downloads are allowed", "refusing non-https URL scheme %q", u.Scheme)
	}
	if u.User != nil {
		return errf("insecure_url", "", "refusing a URL that carries credentials")
	}
	if !c.allow(u.Host) {
		return errf("host_not_allowed", "only huggingface.co and *.hf.co are allowed", "host %q is not allowed", u.Host)
	}
	return nil
}

func escapeRepo(repo string) (string, error) {
	if !repoName.MatchString(repo) {
		return "", errf("invalid_arguments", "use owner/name, e.g. mlx-community/Qwen3-4B-4bit", "invalid Hugging Face repository %q", repo)
	}
	parts := strings.SplitN(repo, "/", 2)
	return url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]), nil
}

// get performs one GET with the safety rules; the caller closes the body.
func (c *HFClient) get(ctx context.Context, u *url.URL, hdr map[string]string) (*http.Response, error) {
	if e := c.checkURL(u); e != nil {
		return nil, e
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "nexal-connector")
	req.Header.Set("Accept-Encoding", "identity") // byte counts and Range must refer to stored bytes
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var ie *Error
		if errors.As(err, &ie) {
			return nil, ie
		}
		if ctx.Err() != nil {
			return nil, cancelled()
		}
		return nil, errf("network", "check the network connection and try again", "download failed: %v", scrubURLError(err))
	}
	return resp, nil
}

func scrubURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func cancelled() *Error {
	return errf("cancelled", "run the command again to resume; partial downloads are kept", "cancelled")
}

func (c *HFClient) apiURL(repo, tail string) (*url.URL, error) {
	er, err := escapeRepo(repo)
	if err != nil {
		return nil, err
	}
	u := *c.base
	u.Path = "/api/models/" + er + tail
	u.RawPath = u.Path
	u.RawQuery = ""
	return &u, nil
}

func (c *HFClient) getJSON(ctx context.Context, u *url.URL, v any) (http.Header, error) {
	resp, err := c.get(ctx, u, map[string]string{"Accept": "application/json"})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, errf("repo_not_found", "check the repository name and revision (--repo, --revision)", "Hugging Face has no such repository or revision")
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, errf("repo_not_public", "only public, ungated repositories can be used; no credentials are ever sent", "the repository requires authentication (HTTP %d)", resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return nil, errf("http_status", "try again later", "Hugging Face answered HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, cancelled()
		}
		return nil, errf("network", "", "reading the API response failed")
	}
	if len(data) > maxAPIBytes {
		return nil, errf("api_too_large", "", "the API response is too large")
	}
	if err := json.Unmarshal(data, v); err != nil {
		return nil, errf("api_invalid", "", "the API response was not valid JSON")
	}
	return resp.Header, nil
}

// RepoInfo is what `pin` needs from the revision metadata.
type RepoInfo struct {
	SHA     string
	License string
	Gated   bool
}

// Info reads the model metadata at an exact revision.
func (c *HFClient) Info(ctx context.Context, repo, rev string) (RepoInfo, error) {
	u, err := c.apiURL(repo, "/revision/"+url.PathEscape(rev))
	if err != nil {
		return RepoInfo{}, err
	}
	var raw struct {
		SHA      string          `json:"sha"`
		Gated    json.RawMessage `json:"gated"`
		Private  bool            `json:"private"`
		Tags     []string        `json:"tags"`
		CardData struct {
			License json.RawMessage `json:"license"`
		} `json:"cardData"`
	}
	if _, err := c.getJSON(ctx, u, &raw); err != nil {
		return RepoInfo{}, err
	}
	info := RepoInfo{SHA: raw.SHA}
	if g := strings.TrimSpace(string(raw.Gated)); g != "" && g != "false" && g != "null" {
		info.Gated = true
	}
	if raw.Private {
		info.Gated = true
	}
	var s string
	if json.Unmarshal(raw.CardData.License, &s) == nil && s != "" {
		info.License = s
	} else {
		var list []string
		if json.Unmarshal(raw.CardData.License, &list) == nil && len(list) == 1 {
			info.License = list[0]
		}
	}
	if info.License == "" {
		for _, t := range raw.Tags {
			if strings.HasPrefix(t, "license:") && len(t) > len("license:") {
				info.License = strings.TrimPrefix(t, "license:")
				break
			}
		}
	}
	return info, nil
}

// TreeEntry is one entry of the repository tree.
type TreeEntry struct {
	Type string `json:"type"`
	Path string `json:"path"`
	Size int64  `json:"size"`
	OID  string `json:"oid"`
	LFS  *struct {
		OID  string `json:"oid"`
		Size int64  `json:"size"`
	} `json:"lfs"`
}

// Tree lists the top level of the repository at an exact revision. A paginated
// listing is refused rather than silently truncated.
func (c *HFClient) Tree(ctx context.Context, repo, rev string) ([]TreeEntry, error) {
	u, err := c.apiURL(repo, "/tree/"+url.PathEscape(rev))
	if err != nil {
		return nil, err
	}
	var out []TreeEntry
	hdr, err := c.getJSON(ctx, u, &out)
	if err != nil {
		return nil, err
	}
	for _, l := range hdr.Values("Link") {
		if strings.Contains(l, `rel="next"`) {
			return nil, errf("tree_paginated", "the repository has too many files to pin safely", "the repository listing is paginated")
		}
	}
	return out, nil
}

// ValidRemoteName reports whether a repository file name is a plain allowlisted
// file name. It is the path-traversal guard for every download.
func ValidRemoteName(name string) bool {
	if name == "" || len(name) > 128 || strings.ContainsAny(name, "/\\\x00") || name == "." || name == ".." || strings.Contains(name, "..") {
		return false
	}
	return AllowedModelFile(name)
}

func (c *HFClient) resolveURL(repo, rev, name string) (*url.URL, error) {
	er, err := escapeRepo(repo)
	if err != nil {
		return nil, err
	}
	if !hexRev.MatchString(rev) {
		return nil, errf("mutable_revision", "pins must name a 40-hex commit", "refusing to download from a mutable revision %q", rev)
	}
	if !ValidRemoteName(name) {
		return nil, errf("bad_file_name", "", "refusing file name %q", name)
	}
	u := *c.base
	u.Path = "/" + er + "/resolve/" + rev + "/" + url.PathEscape(name)
	u.RawPath = u.Path
	u.RawQuery = ""
	return &u, nil
}

// StreamHash downloads one file without storing it, returning its SHA-256. The
// body is capped at size bytes (overflow is an error). When keep is true the
// bytes are also returned (callers use it only for small metadata files).
func (c *HFClient) StreamHash(ctx context.Context, repo, rev, name string, size int64, keep bool, onBytes func(int64)) (string, []byte, error) {
	u, err := c.resolveURL(repo, rev, name)
	if err != nil {
		return "", nil, err
	}
	resp, err := c.get(ctx, u, nil)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil, statusError(resp.StatusCode)
	}
	if resp.ContentLength > size {
		return "", nil, errf("size_overflow", "", "%s is larger than the size Hugging Face listed", name)
	}
	h := sha256.New()
	var buf []byte
	if keep {
		if size > MaxJSONFileBytes {
			return "", nil, errf("size_overflow", "", "%s is too large to inspect", name)
		}
		buf = make([]byte, 0, size)
	}
	chunk := make([]byte, 256<<10)
	var n int64
	for {
		m, rerr := resp.Body.Read(chunk)
		if m > 0 {
			n += int64(m)
			if n > size {
				return "", nil, errf("size_overflow", "", "%s is larger than the size Hugging Face listed", name)
			}
			h.Write(chunk[:m])
			if keep {
				buf = append(buf, chunk[:m]...)
			}
			if onBytes != nil {
				onBytes(n)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			if ctx.Err() != nil {
				return "", nil, cancelled()
			}
			return "", nil, errf("network", "try again", "download of %s was interrupted", name)
		}
	}
	if n != size {
		return "", nil, errf("size_mismatch", "", "%s is %d bytes, expected %d", name, n, size)
	}
	return hex.EncodeToString(h.Sum(nil)), buf, nil
}

func statusError(code int) *Error {
	switch code {
	case http.StatusNotFound:
		return errf("file_not_found", "the pinned revision no longer has this file; re-pin", "the file is not at the pinned revision (HTTP 404)")
	case http.StatusUnauthorized, http.StatusForbidden:
		return errf("repo_not_public", "only public, ungated repositories can be used", "access denied (HTTP %d)", code)
	}
	return errf("http_status", "try again later", "Hugging Face answered HTTP %d", code)
}

// DownloadFile fetches one pinned file into dir. Contract:
//
//   - bytes go to <name>.part (0600) and are hashed as they arrive;
//   - an existing .part is resumed with a Range request (a server that ignores
//     Range restarts the file from zero); a finished file already in place is
//     re-hashed and reused;
//   - the body can never exceed f.Size: an overflow deletes the .part;
//   - the SHA-256 must match before the single atomic rename to <name> (0400);
//     a mismatch deletes the .part, so no unverified file is ever left under
//     the final name;
//   - cancellation or a dropped connection keeps the .part for resuming.
func (c *HFClient) DownloadFile(ctx context.Context, repo, rev string, f PinFile, dir string, onBytes func(done int64)) error {
	if !ValidRemoteName(f.Name) {
		return errf("bad_file_name", "", "refusing file name %q", f.Name)
	}
	if f.Size <= 0 || !hex64.MatchString(f.SHA256) {
		return errf("not_installable", "", "%s has no pinned size and SHA-256", f.Name)
	}
	u, err := c.resolveURL(repo, rev, f.Name)
	if err != nil {
		return err
	}
	final := filepath.Join(dir, f.Name)
	part := final + ".part"

	if st, err := os.Lstat(final); err == nil {
		if st.Mode().IsRegular() && st.Size() == f.Size {
			if sum, herr := hashFile(ctx, final); herr == nil && sum == f.SHA256 {
				if onBytes != nil {
					onBytes(f.Size)
				}
				return nil
			}
		}
		if err := os.Remove(final); err != nil {
			return err
		}
	}
	return c.fetchPart(ctx, u, f, part, final, onBytes, true)
}

func (c *HFClient) fetchPart(ctx context.Context, u *url.URL, f PinFile, part, final string, onBytes func(int64), canRestart bool) error {
	var have int64
	h := sha256.New()
	if st, err := os.Lstat(part); err == nil {
		if !st.Mode().IsRegular() || st.Size() > f.Size {
			if err := os.Remove(part); err != nil {
				return err
			}
		} else {
			have = st.Size()
		}
	}
	file, err := os.OpenFile(part, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	closed := false
	closeFile := func() {
		if !closed {
			file.Close()
			closed = true
		}
	}
	defer closeFile()
	restart := func() error {
		have = 0
		h.Reset()
		if err := file.Truncate(0); err != nil {
			return err
		}
		_, err := file.Seek(0, io.SeekStart)
		return err
	}
	if have > 0 {
		if _, err := io.Copy(h, file); err != nil { // file offset ends at have
			return err
		}
	}
	drop := func() { closeFile(); os.Remove(part) }

	if have < f.Size {
		hdr := map[string]string{}
		if have > 0 {
			hdr["Range"] = "bytes=" + strconv.FormatInt(have, 10) + "-"
		}
		resp, err := c.get(ctx, u, hdr)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusOK:
			if have > 0 { // Range ignored: start over, do not trust the old prefix
				if err := restart(); err != nil {
					return err
				}
			}
			if resp.ContentLength > f.Size {
				drop()
				return errf("size_overflow", "re-pin the model; the file is larger than its pinned size", "%s is larger than its pinned size", f.Name)
			}
		case http.StatusPartialContent:
			var start, total int64
			if m := contentRange.FindStringSubmatch(resp.Header.Get("Content-Range")); m == nil {
				start, total = -1, -1
			} else {
				start, _ = strconv.ParseInt(m[1], 10, 64)
				total, _ = strconv.ParseInt(m[3], 10, 64)
			}
			if start != have || total != f.Size {
				drop()
				return errf("bad_range", "run the install again", "the server answered a resume request inconsistently")
			}
		case http.StatusRequestedRangeNotSatisfiable:
			drop()
			if canRestart {
				return c.fetchPart(ctx, u, f, part, final, onBytes, false)
			}
			return errf("bad_range", "run the install again", "the server refused to resume")
		default:
			return statusError(resp.StatusCode)
		}
		if onBytes != nil && have > 0 {
			onBytes(have)
		}
		remaining := f.Size - have
		chunk := make([]byte, 256<<10)
		var got int64
		for {
			m, rerr := resp.Body.Read(chunk)
			if m > 0 {
				got += int64(m)
				if got > remaining {
					drop()
					return errf("size_overflow", "re-pin the model; the file is larger than its pinned size", "%s exceeded its pinned size of %d bytes", f.Name, f.Size)
				}
				if _, werr := file.Write(chunk[:m]); werr != nil {
					return werr
				}
				h.Write(chunk[:m])
				if onBytes != nil {
					onBytes(have + got)
				}
			}
			if rerr == io.EOF {
				break
			}
			if rerr != nil {
				if ctx.Err() != nil {
					return cancelled()
				}
				return errf("network", "run the install again to resume", "download of %s was interrupted", f.Name)
			}
		}
		if got != remaining {
			return errf("download_incomplete", "run the install again to resume", "download of %s ended early (%d of %d bytes)", f.Name, have+got, f.Size)
		}
	}
	if err := file.Sync(); err != nil {
		return err
	}
	closeFile()
	if sum := hex.EncodeToString(h.Sum(nil)); sum != f.SHA256 {
		os.Remove(part)
		return errf("hash_mismatch", "the file does not match its pin; do not install it. Re-pin only after reviewing the upstream change", "%s failed its SHA-256 check", f.Name)
	}
	if err := os.Chmod(part, 0o400); err != nil {
		return err
	}
	return os.Rename(part, final)
}

// hashFile returns the SHA-256 of a regular file, aborting on cancellation.
func hashFile(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 1<<20)
	for {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		n, err := f.Read(buf)
		h.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
