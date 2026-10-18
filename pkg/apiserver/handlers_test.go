package apiserver

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nalajala4naresh/chvmm-api/subresources"
	v1beta1 "github.com/nalajala4naresh/chvmm-api/v1beta1"
	"github.com/nalajala4naresh/chvmm-api/vmclient"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/nalajala4naresh/ch-operator/pkg/daemon/access"
	"github.com/nalajala4naresh/ch-operator/pkg/vmaccess"
)

const (
	testNode       = "node-a"
	testServerName = "ch-vmm-daemon.ch-vmm-system.svc"
)

// TestConsoleEndToEnd streams a console session from the vmclient SDK through the ch-vmm-gateway
// handler and a real daemon access server (mTLS) to a fake cloud-hypervisor serial socket.
func TestConsoleEndToEnd(t *testing.T) {
	pki := newTestPKI(t)
	serialSock := fakeSerialConsole(t)
	daemonAddr := startDaemon(t, pki, "ch-vmm-gateway", func(types.UID) string { return serialSock })

	c := newFakeClient(t, runningVM(), daemonPod(daemonAddr.IP.String()))
	api := httptest.NewServer(newTestHandler(t, c, pki.dir("ch-vmm-gateway"), daemonAddr.Port))
	defer api.Close()

	conn, err := sdk(t, api.URL).Console(context.Background(), "vm1")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, len("echo:hello"))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "echo:hello" {
		t.Fatalf("got %q, want %q", got, "echo:hello")
	}
}

// TestDaemonRejectsOtherClients checks that the daemon only accepts ch-vmm-gateway's certificate,
// even when another certificate is signed by the same CA.
func TestDaemonRejectsOtherClients(t *testing.T) {
	pki := newTestPKI(t)
	daemonAddr := startDaemon(t, pki, "ch-vmm-gateway", func(types.UID) string { return "/nonexistent" })

	c := newFakeClient(t, runningVM(), daemonPod(daemonAddr.IP.String()))
	api := httptest.NewServer(newTestHandler(t, c, pki.dir("intruder"), daemonAddr.Port))
	defer api.Close()

	if _, err := sdk(t, api.URL).Console(context.Background(), "vm1"); !apierrors.IsServiceUnavailable(err) {
		t.Fatalf("got %v, want ServiceUnavailable", err)
	}
}

// TestDaemonErrorsAreTyped checks that errors from the daemon reach SDK callers as API errors.
func TestDaemonErrorsAreTyped(t *testing.T) {
	pki := newTestPKI(t)
	daemonAddr := startDaemon(t, pki, "ch-vmm-gateway", func(types.UID) string { return "/nonexistent" })

	c := newFakeClient(t, runningVM(), daemonPod(daemonAddr.IP.String()))
	api := httptest.NewServer(newTestHandler(t, c, pki.dir("ch-vmm-gateway"), daemonAddr.Port))
	defer api.Close()
	vms := sdk(t, api.URL)

	// No serial socket, as for VMs started with a TTY console.
	if _, err := vms.Console(context.Background(), "vm1"); !apierrors.IsConflict(err) {
		t.Fatalf("console: got %v, want Conflict", err)
	}

	// Nothing listens on the guest port.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedPort := l.Addr().(*net.TCPAddr).Port
	l.Close()
	_, err = vms.PortForward(context.Background(), "vm1", closedPort)
	if !apierrors.IsServiceUnavailable(err) || !strings.Contains(err.Error(), "connect to VM port") {
		t.Fatalf("portforward: got %v, want ServiceUnavailable from the daemon", err)
	}
}

func TestPortForwardEndToEnd(t *testing.T) {
	pki := newTestPKI(t)
	daemonAddr := startDaemon(t, pki, "ch-vmm-gateway", nil)

	// The "guest" listens on the VM pod IP (loopback here).
	guest, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer guest.Close()
	go func() {
		conn, err := guest.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 64)
		n, _ := conn.Read(buf)
		_, _ = conn.Write([]byte(strings.ToUpper(string(buf[:n]))))
	}()
	guestPort := guest.Addr().(*net.TCPAddr).Port

	vmPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "vm1-pod", Namespace: "default"},
		Status:     corev1.PodStatus{PodIP: "127.0.0.1"},
	}
	c := newFakeClient(t, runningVM(), daemonPod(daemonAddr.IP.String()), vmPod)
	api := httptest.NewServer(newTestHandler(t, c, pki.dir("ch-vmm-gateway"), daemonAddr.Port))
	defer api.Close()

	conn, err := sdk(t, api.URL).PortForward(context.Background(), "vm1", guestPort)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, 4)
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "PING" {
		t.Fatalf("got %q, want PING", got)
	}
}

func TestActions(t *testing.T) {
	tests := []struct {
		name       string
		phase      v1beta1.VirtualMachinePhase
		path       string
		wantCode   int
		wantPhase  v1beta1.VirtualMachinePhase
		wantReboot bool
	}{
		{name: "pause running", phase: v1beta1.VirtualMachineRunning, path: "pause", wantCode: http.StatusAccepted, wantPhase: v1beta1.VirtualMachinePaused},
		{name: "pause paused", phase: v1beta1.VirtualMachinePaused, path: "pause", wantCode: http.StatusConflict, wantPhase: v1beta1.VirtualMachinePaused},
		{name: "unpause paused", phase: v1beta1.VirtualMachinePaused, path: "unpause", wantCode: http.StatusAccepted, wantPhase: v1beta1.VirtualMachineResumed},
		{name: "restart running", phase: v1beta1.VirtualMachineRunning, path: "restart", wantCode: http.StatusAccepted, wantPhase: v1beta1.VirtualMachineRunning, wantReboot: true},
		{name: "restart pending", phase: v1beta1.VirtualMachinePending, path: "restart", wantCode: http.StatusConflict, wantPhase: v1beta1.VirtualMachinePending},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := runningVM()
			vm.Status.Phase = tt.phase
			c := newFakeClient(t, vm)
			h := NewHandler(c, DaemonConfig{})

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/apis/"+subresources.GroupName+"/v1beta1/namespaces/default/virtualmachines/vm1/"+tt.path, nil)
			h.ServeHTTP(rec, req)
			if rec.Code != tt.wantCode {
				t.Fatalf("code = %d, want %d: %s", rec.Code, tt.wantCode, rec.Body)
			}

			var got v1beta1.VirtualMachine
			if err := c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "vm1"}, &got); err != nil {
				t.Fatal(err)
			}
			if got.Status.Phase != tt.wantPhase {
				t.Errorf("phase = %q, want %q", got.Status.Phase, tt.wantPhase)
			}
			if (got.Status.PowerAction == v1beta1.VirtualMachineReboot) != tt.wantReboot {
				t.Errorf("power action = %q, want reboot=%v", got.Status.PowerAction, tt.wantReboot)
			}
		})
	}
}

func TestMigrateCreatesMigration(t *testing.T) {
	c := newFakeClient(t, runningVM())
	api := httptest.NewServer(NewHandler(c, DaemonConfig{}))
	defer api.Close()

	got, err := sdk(t, api.URL).Migrate(context.Background(), "vm1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.VMName != "vm1" || got.Kind != "VirtualMachineMigration" || got.APIVersion != v1beta1.GroupVersion.String() {
		t.Fatalf("unexpected response: %+v", got)
	}
	var migrations v1beta1.VirtualMachineMigrationList
	if err := c.List(context.Background(), &migrations, client.InNamespace("default")); err != nil {
		t.Fatal(err)
	}
	if len(migrations.Items) != 1 || migrations.Items[0].Name != got.Name {
		t.Fatalf("unexpected migrations: %+v", migrations.Items)
	}
}

func TestDiscovery(t *testing.T) {
	h := NewHandler(newFakeClient(t), DaemonConfig{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/apis/"+subresources.GroupName+"/v1beta1", nil))
	var list metav1.APIResourceList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range list.APIResources {
		names = append(names, r.Name)
	}
	if !strings.Contains(strings.Join(names, ","), "virtualmachines/console") {
		t.Fatalf("console missing from discovery: %v", names)
	}
}

func TestStreamingRequiresRunningVM(t *testing.T) {
	vm := runningVM()
	vm.Status.Phase = v1beta1.VirtualMachineScheduling
	h := NewHandler(newFakeClient(t, vm), DaemonConfig{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/apis/"+subresources.GroupName+"/v1beta1/namespaces/default/virtualmachines/vm1/console", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("code = %d, want 409", rec.Code)
	}
}

// --- helpers ---

func runningVM() *v1beta1.VirtualMachine {
	return &v1beta1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{Name: "vm1", Namespace: "default"},
		Status: v1beta1.VirtualMachineStatus{
			Phase:     v1beta1.VirtualMachineRunning,
			NodeName:  testNode,
			VMPodName: "vm1-pod",
			VMPodUID:  "pod-uid",
		},
	}
}

func daemonPod(ip string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "ch-vmm-daemon-x", Namespace: "ch-vmm-system", Labels: map[string]string{"name": "ch-vmm-daemon"}},
		Spec:       corev1.PodSpec{NodeName: testNode},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, PodIP: ip},
	}
}

func newFakeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1beta1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&v1beta1.VirtualMachine{}).
		WithIndex(&corev1.Pod{}, "spec.nodeName", func(o client.Object) []string {
			return []string{o.(*corev1.Pod).Spec.NodeName}
		}).
		Build()
}

func newTestHandler(t *testing.T, c client.Client, clientCertDir string, daemonPort int) *Handler {
	t.Helper()
	tlsConfig, err := vmaccess.ClientTLSConfig(clientCertDir, testServerName)
	if err != nil {
		t.Fatal(err)
	}
	return NewHandler(c, DaemonConfig{
		Namespace:  "ch-vmm-system",
		Selector:   labels.SelectorFromSet(labels.Set{"name": "ch-vmm-daemon"}),
		Port:       daemonPort,
		ServerName: testServerName,
		Transport:  &http.Transport{TLSClientConfig: tlsConfig},
	})
}

func startDaemon(t *testing.T, pki *testPKI, allowedCN string, serialPath func(types.UID) string) *net.TCPAddr {
	t.Helper()
	tlsConfig, err := vmaccess.ServerTLSConfig(pki.dir("daemon"), allowedCN)
	if err != nil {
		t.Fatal(err)
	}
	vmPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "vm1-pod", Namespace: "default"},
		Status:     corev1.PodStatus{PodIP: "127.0.0.1"},
	}
	srv := &access.Server{
		Client:           newFakeClient(t, runningVM(), vmPod),
		NodeName:         testNode,
		SerialSocketPath: serialPath,
	}
	ts := httptest.NewUnstartedServer(srv.Handler())
	ts.TLS = tlsConfig
	ts.StartTLS()
	t.Cleanup(ts.Close)
	return ts.Listener.Addr().(*net.TCPAddr)
}

// fakeSerialConsole listens on a Unix socket like cloud-hypervisor's serial socket and echoes
// what it receives with an "echo:" prefix.
func fakeSerialConsole(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ser")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "s.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 64)
		n, _ := conn.Read(buf)
		_, _ = conn.Write(append([]byte("echo:"), buf[:n]...))
		time.Sleep(time.Second)
	}()
	return path
}

// sdk returns a vmclient for VMs in "default" talking to the handler at apiURL. The handler
// stands in for kube-apiserver, so no credentials are needed.
func sdk(t *testing.T, apiURL string) vmclient.VirtualMachineInterface {
	t.Helper()
	c, err := vmclient.NewForConfig(&rest.Config{Host: apiURL})
	if err != nil {
		t.Fatal(err)
	}
	return c.VirtualMachines("default")
}

// testPKI issues a CA and leaf certificates into cert-manager style directories.
type testPKI struct {
	root   string
	ca     *x509.Certificate
	caKey  *ecdsa.PrivateKey
	caPEM  []byte
	serial int64
}

func newTestPKI(t *testing.T) *testPKI {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(der)
	p := &testPKI{root: t.TempDir(), ca: ca, caKey: key, serial: 1,
		caPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}

	p.issue(t, "daemon", testServerName, []string{testServerName}, []net.IP{net.ParseIP("127.0.0.1")},
		[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	p.issue(t, "ch-vmm-gateway", "ch-vmm-gateway", nil, nil, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
	p.issue(t, "intruder", "intruder", nil, nil, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
	return p
}

func (p *testPKI) dir(name string) string { return filepath.Join(p.root, name) }

func (p *testPKI) issue(t *testing.T, name, cn string, dns []string, ips []net.IP, usages []x509.ExtKeyUsage) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p.serial++
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(p.serial),
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     dns,
		IPAddresses:  ips,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  usages,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.ca, &key.PublicKey, p.caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := p.dir(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(file string, data []byte) {
		if err := os.WriteFile(filepath.Join(dir, file), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("tls.crt", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	write("tls.key", pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	write("ca.crt", p.caPEM)
}
