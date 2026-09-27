package pool

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"nexal/connector/internal/observability"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const peerResponseLimit = 16 << 10

// PeerOptions is an explicit opt-in to a private object-transfer endpoint.
// AllowedPeers is an exact device-fingerprint allowlist, not "trust the LAN".
// All allowed peers can read/write this Store; use a dedicated store per trust
// group. Project-level ACL filtering is NOT implemented by this host endpoint.
type PeerOptions struct {
	Identity     Identity
	Registry     *Registry
	AllowedPeers []string
	Store        *Store
	Clock        func() time.Time
}

type peerPolicy struct {
	registry *Registry
	allowed  map[string]bool
	now      func() time.Time
}

func newPeerPolicy(registry *Registry, allowed []string, clock func() time.Time) (*peerPolicy, error) {
	if registry == nil || len(allowed) < 1 || len(allowed) > 128 {
		return nil, ErrInvalid
	}
	if clock == nil {
		clock = time.Now
	}
	p := &peerPolicy{registry, make(map[string]bool), clock}
	for _, id := range allowed {
		if !validDigest(id) || p.allowed[id] {
			return nil, ErrInvalid
		}
		if err := p.enrolled(id); err != nil {
			return nil, err
		}
		p.allowed[id] = true
	}
	return p, nil
}

func (p *peerPolicy) enrolled(id string) error {
	m, ok := p.registry.Member(id)
	if !ok || m.RevokedAt != nil || !hasRole(m, Contributor) {
		return ErrUnauthorized
	}
	return nil
}

func (p *peerPolicy) verifyCertificates(certs []*x509.Certificate) error {
	if len(certs) != 1 {
		return ErrUnauthorized
	}
	cert := certs[0]
	key, ok := cert.PublicKey.(ed25519.PublicKey)
	if !ok || !p.allowed[DeviceID(key)] {
		return ErrUnauthorized
	}
	if err := p.enrolled(DeviceID(key)); err != nil {
		return err
	}
	now := p.now()
	if now.Before(cert.NotBefore) || !now.Before(cert.NotAfter) ||
		cert.SignatureAlgorithm != x509.PureEd25519 ||
		cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature) != nil {
		return ErrUnauthorized
	}
	return nil
}

const (
	// A device certificate is deliberately short lived: it is a handshake binding
	// for an enrolled Ed25519 device, not a durable credential.
	peerCertificateLifetime = 24 * time.Hour
	// Re-mint this far ahead of expiry so an in-progress transfer cannot be cut
	// off by its own certificate aging out mid-connection.
	peerCertificateRenewal = time.Hour
)

func peerCertificate(identity Identity, now time.Time) (tls.Certificate, error) {
	if !identity.valid() || now.IsZero() {
		return tls.Certificate{}, ErrInvalid
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: DeviceID(identity.PublicKey)},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(peerCertificateLifetime),
		KeyUsage:           x509.KeyUsageDigitalSignature,
		ExtKeyUsage:        []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		SignatureAlgorithm: x509.PureEd25519,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, identity.PublicKey, identity.PrivateKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	// Parse the leaf so rotation reads the real validity window instead of
	// recomputing it, and so the TLS stack does not re-parse on every handshake.
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: identity.PrivateKey, Leaf: leaf}, nil
}

// peerCertificates mints this device's certificate on demand and caches it until
// it approaches expiry. A certificate minted once at construction expires after
// peerCertificateLifetime, after which a long-running host presents a certificate
// its own peers must reject (verifyCertificates checks NotAfter) and every
// transfer fails until somebody restarts the process. Rotation is local: the
// certificate is self-signed by the enrolled device key, so no re-enrollment,
// registry round trip or new trust decision is involved.
type peerCertificates struct {
	identity Identity
	now      func() time.Time
	mu       sync.Mutex
	current  *tls.Certificate
}

func (c *peerCertificates) certificate() (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if current := c.current; current != nil && current.Leaf != nil &&
		!now.Before(current.Leaf.NotBefore) &&
		now.Add(peerCertificateRenewal).Before(current.Leaf.NotAfter) {
		return current, nil
	}
	// Also covers a clock that moved backwards past the cached NotBefore: the
	// cached certificate is not yet valid, so mint one for the time we now see.
	minted, err := peerCertificate(c.identity, now)
	if err != nil {
		c.current = nil
		return nil, err
	}
	c.current = &minted
	return c.current, nil
}

func peerTLS(identity Identity, policy *peerPolicy, server bool) (*tls.Config, error) {
	if err := policy.enrolled(DeviceID(identity.PublicKey)); err != nil {
		return nil, err
	}
	certificates := &peerCertificates{identity: identity, now: policy.now}
	// Mint eagerly so an unusable identity or a broken entropy source is reported
	// here rather than as an opaque handshake failure later.
	if _, err := certificates.certificate(); err != nil {
		return nil, err
	}
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
		// Require P-384 + standardized ML-KEM-1024 on both ends. Nil preferences
		// would permit ML-KEM-768 and classical groups; no weaker fallback is
		// allowed here. This is the peer transport, not the Rosenpass VPN.
		// Device authentication remains classical Ed25519.
		CurvePreferences: []tls.CurveID{tls.SecP384r1MLKEM1024},
		// Certificates is left empty on purpose. Go consults GetCertificate only
		// when Certificates is empty or the handshake carried SNI, and peers dial
		// literal private IPs, which send no SNI: a populated Certificates slice
		// would pin the process to the certificate minted at startup forever.
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return certificates.certificate()
		},
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return certificates.certificate()
		},
		// Names/public CAs are not the trust model. The mandatory callback below
		// verifies a pinned, enrolled Ed25519 device, cert validity and signature.
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if cs.Version != tls.VersionTLS13 || cs.CurveID != tls.SecP384r1MLKEM1024 {
				return ErrUnauthorized
			}
			return policy.verifyCertificates(cs.PeerCertificates)
		},
		Time:                   policy.now,
		NextProtos:             []string{"http/1.1"},
		SessionTicketsDisabled: true,
	}
	if server {
		cfg.ClientAuth = tls.RequireAnyClientCert
		cfg.InsecureSkipVerify = false // Server side verifies clients in callback.
	}
	return cfg, nil
}

// PeerServer never binds or starts itself. Serve requires an already-bound,
// explicit private/loopback unicast interface, not 0.0.0.0 or ::. The caller
// controls lifecycle. No command/job execution endpoints are included.
type PeerServer struct {
	server *http.Server
	tls    *tls.Config
	mu     sync.Mutex
	served bool
}

func NewPeerServer(o PeerOptions) (*PeerServer, error) {
	if o.Store == nil {
		return nil, ErrInvalid
	}
	policy, err := newPeerPolicy(o.Registry, o.AllowedPeers, o.Clock)
	if err != nil {
		return nil, err
	}
	cfg, err := peerTLS(o.Identity, policy, true)
	if err != nil {
		return nil, err
	}
	// Bound simultaneous object requests as well as bytes/time. Storage's mutex
	// supplies atomicity; this gate avoids unbounded queued transfer goroutines.
	gate := make(chan struct{}, 8)
	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if req.TLS == nil || req.TLS.Version != tls.VersionTLS13 ||
			policy.enrolled(DeviceID(o.Identity.PublicKey)) != nil ||
			policy.verifyCertificates(req.TLS.PeerCertificates) != nil {
			peerError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		select {
		case gate <- struct{}{}:
			defer func() { <-gate }()
		default:
			peerError(w, http.StatusServiceUnavailable, "busy")
			return
		}
		if req.URL.RawQuery != "" || req.URL.RawPath != "" {
			peerError(w, http.StatusBadRequest, "invalid_path")
			return
		}
		parts := strings.Split(req.URL.Path, "/")
		if len(parts) != 5 || parts[0] != "" || parts[1] != "v1" || parts[2] != "objects" {
			peerError(w, http.StatusNotFound, "not_found")
			return
		}
		class, digest := StorageClass(parts[3]), parts[4]
		if !class.valid() || !validDigest(digest) {
			peerError(w, http.StatusBadRequest, "invalid_object")
			return
		}
		switch req.Method {
		case http.MethodPut:
			if req.ContentLength < 0 || req.ContentLength > o.Store.maxObject || req.Header.Get("Content-Encoding") != "" {
				peerError(w, http.StatusRequestEntityTooLarge, "invalid_size")
				return
			}
			req.Body = http.MaxBytesReader(w, req.Body, req.ContentLength)
			blob, err := o.Store.Put(class, digest, req.ContentLength, observability.PeerReader{Reader: req.Body, Context: req.Context()})
			if err != nil {
				peerStoreError(w, err)
				return
			}
			receipt, err := o.Store.Receipt(o.Identity, class, digest, policy.now())
			if err != nil {
				peerStoreError(w, err)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(peerAcknowledgment{blob, receipt})
		case http.MethodGet:
			file, size, err := o.Store.openTransfer(class, digest)
			if err != nil {
				peerStoreError(w, err)
				return
			}
			defer file.Close()
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
			w.Header().Set("ETag", `"`+digest+`"`)
			sent, _ := io.Copy(w, file)
			observability.PeerTransfer(req.Context(), "sent", sent)
		default:
			w.Header().Set("Allow", "GET, PUT")
			peerError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 2 * time.Minute, WriteTimeout: 2 * time.Minute, IdleTimeout: 15 * time.Second,
		MaxHeaderBytes: 8 << 10, TLSConfig: cfg}
	return &PeerServer{server: server, tls: cfg}, nil
}

func privateIP(ip net.IP) bool {
	return ip != nil && !ip.IsUnspecified() && !ip.IsMulticast() &&
		(ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast())
}

// ValidPeerEndpoint is the endpoint rule, exported so that a caller which stores
// an owner-configured peer address (internal/config static peers, for cross-VLAN
// dialling) validates against THE SAME code NewPeerClient uses, instead of a
// second copy that can drift looser. NewPeerClient calls it too, so there is one
// rule and one place to read it.
//
// The rule is unchanged and is deliberately not relaxed for cross-VLAN use: an
// RFC1918 address on another subnet already satisfies ip.IsPrivate(), so routed
// inter-VLAN peer transport needs no loosening here — only an address to dial.
// A public address is still refused, which is what keeps this transport off the
// open internet until a reviewed NAT-traversal design lands
// (TRANSPORT-NAT-DESIGN.md).
//
// Being able to dial an address is never permission to talk to whoever answers:
// the mutual-TLS peerPolicy pins an exact device fingerprint on every handshake.
func ValidPeerEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" ||
		u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Port() == "" ||
		!privateIP(net.ParseIP(u.Hostname())) {
		return ErrUnsafePath
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return ErrInvalid
	}
	return nil
}

// Serve takes ownership of listener only after validating the bound address.
func (p *PeerServer) Serve(listener net.Listener) error {
	if listener == nil {
		return ErrInvalid
	}
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !privateIP(addr.IP) {
		return fmt.Errorf("%w: explicitly bind a private unicast interface", ErrUnsafePath)
	}
	p.mu.Lock()
	if p.served {
		p.mu.Unlock()
		return ErrConflict
	}
	p.served = true
	p.mu.Unlock()
	bounded := &boundedListener{Listener: listener, slots: make(chan struct{}, 64)}
	return p.server.Serve(tls.NewListener(bounded, p.tls))
}

func (p *PeerServer) Shutdown(ctx context.Context) error { return p.server.Shutdown(ctx) }
func (p *PeerServer) Close() error                       { return p.server.Close() }

// Limit accepted connections before the TLS handshake to bound unauthenticated
// connection work. Excess connections are closed without spawning handlers.
type boundedListener struct {
	net.Listener
	slots chan struct{}
}

func (l *boundedListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case l.slots <- struct{}{}:
			return &boundedConn{Conn: conn, release: func() { <-l.slots }}, nil
		default:
			conn.Close()
		}
	}
}

type boundedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *boundedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

func peerError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": code}})
}

func peerStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, os.ErrNotExist):
		peerError(w, http.StatusNotFound, "not_found")
	case errors.Is(err, ErrQuota), errors.Is(err, ErrHeadroom):
		peerError(w, http.StatusInsufficientStorage, "insufficient_storage")
	case errors.Is(err, ErrIntegrity), errors.Is(err, ErrInvalid):
		peerError(w, http.StatusBadRequest, "integrity_or_size_mismatch")
	default:
		peerError(w, http.StatusConflict, "object_unavailable")
	}
}

func (s *Store) openTransfer(class StorageClass, digest string) (*os.File, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, 0, ErrClosed
	}
	f, err := s.verifiedLocked(class, digest)
	if err != nil {
		return nil, 0, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, info.Size(), nil
}

type peerAcknowledgment struct {
	Blob    Blob           `json:"blob"`
	Receipt ReplicaReceipt `json:"receipt"`
}

type PeerClientOptions struct {
	Identity         Identity
	Registry         *Registry
	ExpectedDeviceID string
	Endpoint         string // HTTPS literal private/loopback IP plus explicit port only.
	MaxObjectBytes   int64
	Clock            func() time.Time
}

type PeerClient struct {
	client    *http.Client
	transport *http.Transport
	policy    *peerPolicy
	identity  string
	device    string
	endpoint  string
	max       int64
}

func NewPeerClient(o PeerClientOptions) (*PeerClient, error) {
	if o.MaxObjectBytes <= 0 || o.MaxObjectBytes >= 1<<63-1 {
		return nil, ErrInvalid
	}
	if err := ValidPeerEndpoint(o.Endpoint); err != nil {
		return nil, err
	}
	policy, err := newPeerPolicy(o.Registry, []string{o.ExpectedDeviceID}, o.Clock)
	if err != nil {
		return nil, err
	}
	cfg, err := peerTLS(o.Identity, policy, false)
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{
		// No environment proxy or DNS lookup: explicit local endpoint only.
		Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 15 * time.Second}).DialContext,
		TLSClientConfig: cfg, TLSHandshakeTimeout: 5 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second, IdleConnTimeout: 15 * time.Second,
		MaxIdleConns: 2, MaxIdleConnsPerHost: 2, MaxConnsPerHost: 2,
		MaxResponseHeaderBytes: 8 << 10, DisableCompression: true,
	}
	client := &http.Client{Transport: tr, Timeout: 2 * time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error { return ErrUnauthorized }}
	return &PeerClient{client, tr, policy, DeviceID(o.Identity.PublicKey),
		o.ExpectedDeviceID, strings.TrimSuffix(o.Endpoint, "/"), o.MaxObjectBytes}, nil
}

func (p *PeerClient) DeviceID() string { return p.device }
func (p *PeerClient) Close()           { p.transport.CloseIdleConnections() }

func (p *PeerClient) authorized() error {
	if err := p.policy.enrolled(p.identity); err != nil {
		return err
	}
	return p.policy.enrolled(p.device)
}

func (p *PeerClient) response(resp *http.Response, allowedStatus int) error {
	if resp.TLS == nil || p.policy.verifyCertificates(resp.TLS.PeerCertificates) != nil {
		return ErrUnauthorized
	}
	if resp.StatusCode != allowedStatus {
		return fmt.Errorf("pool: peer request rejected (HTTP %d)", resp.StatusCode)
	}
	return nil
}

// Upload returns only an authenticated, verified signed acknowledgment. It does
// not count an HTTP success code alone as a durable replica confirmation.
func (p *PeerClient) Upload(ctx context.Context, blob Blob, reader io.Reader) (ReplicaReceipt, error) {
	if !blob.Class.valid() || !validDigest(blob.Digest) || blob.Size < 0 || blob.Size > p.max || reader == nil {
		return ReplicaReceipt{}, ErrInvalid
	}
	if err := p.authorized(); err != nil {
		return ReplicaReceipt{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		p.endpoint+"/v1/objects/"+string(blob.Class)+"/"+blob.Digest, io.LimitReader(reader, blob.Size))
	if err != nil {
		return ReplicaReceipt{}, err
	}
	req.ContentLength = blob.Size
	if blob.Size == 0 {
		req.Body = http.NoBody
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := p.client.Do(req)
	if err != nil {
		return ReplicaReceipt{}, err
	}
	defer resp.Body.Close()
	if err := p.response(resp, http.StatusCreated); err != nil {
		return ReplicaReceipt{}, err
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, peerResponseLimit+1))
	if err != nil {
		return ReplicaReceipt{}, err
	}
	var ack peerAcknowledgment
	if len(data) > peerResponseLimit || strictJSON(data, &ack) != nil ||
		ack.Blob != blob || ack.Receipt.DeviceID != p.device || ack.Receipt.Digest != blob.Digest ||
		ack.Receipt.Size != blob.Size || ack.Receipt.Class != blob.Class {
		return ReplicaReceipt{}, ErrIntegrity
	}
	if err := p.policy.registry.VerifyReceipt(ack.Receipt, p.policy.now(), 5*time.Minute); err != nil {
		return ReplicaReceipt{}, err
	}
	return ack.Receipt, nil
}

// Download verifies the object digest and size while writing a local immutable
// copy. No network response is trusted as plaintext without that check.
func (p *PeerClient) Download(ctx context.Context, destination *Store, blob Blob) error {
	if destination == nil || !blob.Class.valid() || !validDigest(blob.Digest) ||
		blob.Size < 0 || blob.Size > p.max {
		return ErrInvalid
	}
	if err := p.authorized(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		p.endpoint+"/v1/objects/"+string(blob.Class)+"/"+blob.Digest, nil)
	if err != nil {
		return err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := p.response(resp, http.StatusOK); err != nil {
		return err
	}
	if resp.ContentLength != blob.Size || resp.Header.Get("Content-Encoding") != "" {
		return ErrIntegrity
	}
	_, err = destination.Put(blob.Class, blob.Digest, blob.Size, io.LimitReader(resp.Body, blob.Size+1))
	return err
}

// ReplicateProtected makes real authenticated transfers until it has local plus
// remote confirmations from two DISTINCT enrolled device identities. It does not
// publish a manifest or claim independence of hardware solely from key count.
// Enroll one identity per owner-verified machine, not multiple keys on one Mac.
func ReplicateProtected(ctx context.Context, source *Store, identity Identity, registry *Registry,
	digest string, peers []*PeerClient, now time.Time) ([]ReplicaReceipt, error) {
	if source == nil || registry == nil || !validDigest(digest) || len(peers) > 16 {
		return nil, ErrInvalid
	}
	local, err := source.Receipt(identity, Protected, digest, now)
	if err != nil {
		return nil, err
	}
	if err := registry.VerifyReceipt(local, now, 5*time.Minute); err != nil {
		return nil, err
	}
	receipts := []ReplicaReceipt{local}
	seen := map[string]bool{local.DeviceID: true}
	var failures []error
	for _, peer := range peers {
		if peer == nil || seen[peer.device] {
			continue
		}
		seen[peer.device] = true
		file, size, err := source.openTransfer(Protected, digest)
		if err != nil {
			return receipts, err
		}
		remote, err := peer.Upload(ctx, Blob{digest, size, Protected}, file)
		file.Close()
		if err != nil {
			failures = append(failures, err)
			continue
		}
		// Verify against the replication authority too, not just client's registry.
		if err := registry.VerifyReceipt(remote, peer.policy.now(), 5*time.Minute); err != nil {
			failures = append(failures, err)
			continue
		}
		receipts = append(receipts, remote)
		if len(receipts) >= 2 {
			// Transfers/retries may take time; reverify and refresh the local
			// assertion rather than returning an aged initial receipt.
			current := peer.policy.now()
			fresh, err := source.Receipt(identity, Protected, digest, current)
			if err != nil {
				return receipts, err
			}
			if err := registry.VerifyReceipt(fresh, current, 5*time.Minute); err != nil {
				return receipts, err
			}
			receipts[0] = fresh
			return receipts, nil
		}
	}
	return receipts, errors.Join(append([]error{ErrDurability}, failures...)...)
}
