package vmaccess

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// certReloader serves a cert-manager style directory (tls.crt, tls.key, ca.crt) and re-reads it
// when the files change, so rotated certificates are picked up without restarting.
type certReloader struct {
	dir string

	mu      sync.Mutex
	modTime time.Time
	cert    *tls.Certificate
	caPool  *x509.CertPool
}

func newCertReloader(dir string) (*certReloader, error) {
	r := &certReloader{dir: dir}
	if _, _, err := r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *certReloader) load() (*tls.Certificate, *x509.CertPool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	info, err := os.Stat(filepath.Join(r.dir, "tls.crt"))
	if err != nil {
		return nil, nil, fmt.Errorf("stat certificate: %w", err)
	}
	if r.cert != nil && info.ModTime().Equal(r.modTime) {
		return r.cert, r.caPool, nil
	}

	cert, err := tls.LoadX509KeyPair(filepath.Join(r.dir, "tls.crt"), filepath.Join(r.dir, "tls.key"))
	if err != nil {
		return nil, nil, fmt.Errorf("load key pair: %w", err)
	}
	caData, err := os.ReadFile(filepath.Join(r.dir, "ca.crt"))
	if err != nil {
		return nil, nil, fmt.Errorf("read CA certificate: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caData) {
		return nil, nil, fmt.Errorf("no certificates found in %s", filepath.Join(r.dir, "ca.crt"))
	}
	r.cert, r.caPool, r.modTime = &cert, pool, info.ModTime()
	return r.cert, r.caPool, nil
}

// ServerTLSConfig returns a TLS config for the daemon's streaming endpoint. Clients must present
// a certificate issued by the CA in certDir whose common name is allowedClientCN.
func ServerTLSConfig(certDir, allowedClientCN string) (*tls.Config, error) {
	r, err := newCertReloader(certDir)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		// Client verification happens in VerifyConnection so the CA pool can be reloaded.
		ClientAuth: tls.RequireAnyClientCert,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			cert, _, err := r.load()
			return cert, err
		},
		VerifyConnection: func(cs tls.ConnectionState) error {
			_, pool, err := r.load()
			if err != nil {
				return err
			}
			if len(cs.PeerCertificates) == 0 {
				return fmt.Errorf("client certificate required")
			}
			opts := x509.VerifyOptions{
				Roots:         pool,
				Intermediates: x509.NewCertPool(),
				KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
			}
			for _, c := range cs.PeerCertificates[1:] {
				opts.Intermediates.AddCert(c)
			}
			leaf := cs.PeerCertificates[0]
			if _, err := leaf.Verify(opts); err != nil {
				return fmt.Errorf("verify client certificate: %w", err)
			}
			if leaf.Subject.CommonName != allowedClientCN {
				return fmt.Errorf("client %q is not allowed", leaf.Subject.CommonName)
			}
			return nil
		},
	}, nil
}

// ClientTLSConfig returns a TLS config for ch-vmm-gateway dialing daemons: it presents the client
// certificate in certDir and verifies the daemon against the same CA and serverName.
func ClientTLSConfig(certDir, serverName string) (*tls.Config, error) {
	r, err := newCertReloader(certDir)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: serverName,
		// Server verification happens in VerifyConnection so the CA pool can be reloaded.
		InsecureSkipVerify: true, //nolint:gosec // verified below against the reloaded CA pool
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			cert, _, err := r.load()
			return cert, err
		},
		VerifyConnection: func(cs tls.ConnectionState) error {
			_, pool, err := r.load()
			if err != nil {
				return err
			}
			if len(cs.PeerCertificates) == 0 {
				return fmt.Errorf("server certificate required")
			}
			opts := x509.VerifyOptions{
				DNSName:       serverName,
				Roots:         pool,
				Intermediates: x509.NewCertPool(),
			}
			for _, c := range cs.PeerCertificates[1:] {
				opts.Intermediates.AddCert(c)
			}
			_, err = cs.PeerCertificates[0].Verify(opts)
			return err
		},
	}, nil
}
