package vmaccess

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "ch-vmm-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &testCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// issue writes a cert-manager style directory (tls.crt, tls.key, ca.crt) for a leaf signed by ca.
func (ca *testCA) issue(t *testing.T, cn string, dnsNames []string, usages ...x509.ExtKeyUsage) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     dnsNames,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  usages,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	files := map[string][]byte{
		"tls.crt": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		"tls.key": pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		"ca.crt":  ca.pem,
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// handshake runs a TLS handshake over loopback TCP and returns the server and client errors.
func handshake(t *testing.T, server, client *tls.Config) (serverErr, clientErr error) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	serverDone := make(chan error, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		tc := tls.Server(conn, server)
		err = tc.Handshake()
		if err == nil {
			// TLS 1.3 verifies the client certificate after the client's handshake returns, so
			// exchange a byte to surface a rejection on the client side as well.
			_, err = tc.Write([]byte{1})
		}
		serverDone <- err
	}()

	conn, err := tls.Dial("tcp", l.Addr().String(), client)
	if err == nil {
		_, err = conn.Read(make([]byte, 1))
		conn.Close()
	}
	return <-serverDone, err
}

var daemonDNSNames = []string{DaemonServerName, DaemonServerName + ".cluster.local"}

func daemonCert(t *testing.T, ca *testCA) string {
	return ca.issue(t, DaemonCommonName, daemonDNSNames, x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth)
}

func migrationConfigs(t *testing.T, targetDir, sourceDir string) (server, client *tls.Config) {
	t.Helper()
	server, err := ServerTLSConfig(targetDir, DaemonCommonName)
	if err != nil {
		t.Fatal(err)
	}
	client, err = ClientTLSConfig(sourceDir, DaemonServerName)
	if err != nil {
		t.Fatal(err)
	}
	return server, client
}

func TestMigrationTLSBetweenDaemons(t *testing.T) {
	ca := newTestCA(t)
	server, client := migrationConfigs(t, daemonCert(t, ca), daemonCert(t, ca))

	serverErr, clientErr := handshake(t, server, client)
	if serverErr != nil || clientErr != nil {
		t.Fatalf("daemon to daemon handshake failed: server=%v client=%v", serverErr, clientErr)
	}
}

func TestMigrationSourceRejectsTargetFromAnotherCA(t *testing.T) {
	ca, rogueCA := newTestCA(t), newTestCA(t)
	// Same names as a real daemon, but not issued by ch-vmm-ca.
	server, client := migrationConfigs(t, daemonCert(t, rogueCA), daemonCert(t, ca))

	if _, clientErr := handshake(t, server, client); clientErr == nil {
		t.Fatal("source accepted a target certificate from another CA")
	}
}

func TestMigrationSourceRejectsTargetWithoutDaemonName(t *testing.T) {
	ca := newTestCA(t)
	// Issued by ch-vmm-ca, but for another service (for example ch-vmm-gateway's serving cert).
	target := ca.issue(t, "ch-vmm-gateway", []string{"ch-vmm-gateway.ch-vmm-system.svc"},
		x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth)
	server, client := migrationConfigs(t, target, daemonCert(t, ca))

	if _, clientErr := handshake(t, server, client); clientErr == nil {
		t.Fatal("source accepted a target certificate without the daemon server name")
	}
}

func TestMigrationTargetRejectsGatewayClientCert(t *testing.T) {
	ca := newTestCA(t)
	gateway := ca.issue(t, "ch-vmm-gateway", nil, x509.ExtKeyUsageClientAuth)
	server, client := migrationConfigs(t, daemonCert(t, ca), gateway)

	if serverErr, _ := handshake(t, server, client); serverErr == nil {
		t.Fatal("migration target accepted ch-vmm-gateway's client certificate")
	}
}

func TestMigrationTargetRejectsCertWithoutClientAuth(t *testing.T) {
	ca := newTestCA(t)
	// The daemon certificate shape before client auth was added to its usages.
	serverOnly := ca.issue(t, DaemonCommonName, daemonDNSNames, x509.ExtKeyUsageServerAuth)
	server, client := migrationConfigs(t, daemonCert(t, ca), serverOnly)

	if serverErr, _ := handshake(t, server, client); serverErr == nil {
		t.Fatal("migration target accepted a certificate without the client auth usage")
	}
}
