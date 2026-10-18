package apiserver

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	v1beta1 "github.com/nalajala4naresh/chvmm-api/v1beta1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/nalajala4naresh/ch-operator/pkg/vmaccess"
)

// TestServerAuthorization runs ch-vmm-gateway against a real kube-apiserver (envtest) and checks
// that delegated authentication and SubjectAccessReview authorization gate the subresources.
// Callers authenticate with client certificates here instead of through the aggregator.
func TestServerAuthorization(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set; run with envtest binaries (make test)")
	}

	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	admin, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.Stop() })

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1beta1.AddToScheme(scheme))
	c, err := client.New(admin, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	vm := runningVM()
	status := vm.Status
	if err := c.Create(ctx, vm); err != nil {
		t.Fatal(err)
	}
	vm.Status = status
	if err := c.Status().Update(ctx, vm); err != nil {
		t.Fatal(err)
	}

	// alice may pause VMs and open consoles; viewer has "get" on every subresource, which must
	// not be enough to open a console; bob has no permissions.
	grant(t, c, "alice", rbacv1.PolicyRule{
		APIGroups: []string{vmaccess.GroupName},
		Resources: []string{"virtualmachines/pause", "virtualmachines/console"},
		Verbs:     []string{"create"},
	})
	grant(t, c, "viewer", rbacv1.PolicyRule{
		APIGroups: []string{vmaccess.GroupName},
		Resources: []string{"virtualmachines/console", "virtualmachines/portforward"},
		Verbs:     []string{"get", "list", "watch"},
	})
	alice := addUser(t, env, "alice")
	viewer := addUser(t, env, "viewer")
	bob := addUser(t, env, "bob")

	addr := startAPIServer(t, env, admin, c)
	url := "https://" + addr + "/apis/" + vmaccess.GroupName + "/v1beta1/namespaces/default/virtualmachines/vm1/"

	if code := do(t, bob, http.MethodPost, url+"pause"); code != http.StatusForbidden {
		t.Fatalf("bob pause: code %d, want 403", code)
	}
	if code := do(t, alice, http.MethodPost, url+"restart"); code != http.StatusForbidden {
		t.Fatalf("alice restart: code %d, want 403", code)
	}

	// A console handshake is a GET but needs "create". viewer is denied; alice passes
	// authorization and fails later only because no daemon runs in this test (503).
	if code := do(t, viewer, http.MethodGet, url+"console"); code != http.StatusForbidden {
		t.Fatalf("viewer console: code %d, want 403", code)
	}
	if code := do(t, alice, http.MethodGet, url+"console"); code != http.StatusServiceUnavailable {
		t.Fatalf("alice console: code %d, want 503 (authorized, no daemon)", code)
	}

	if code := do(t, alice, http.MethodPost, url+"pause"); code != http.StatusAccepted {
		t.Fatalf("alice pause: code %d, want 202", code)
	}

	var got v1beta1.VirtualMachine
	if err := c.Get(ctx, client.ObjectKeyFromObject(vm), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != v1beta1.VirtualMachinePaused {
		t.Fatalf("phase = %q, want Paused", got.Status.Phase)
	}

	// Discovery is readable by any authenticated user (system:discovery).
	if code := do(t, bob, http.MethodGet, "https://"+addr+"/apis/"+vmaccess.GroupName+"/v1beta1"); code != http.StatusOK {
		t.Fatalf("bob discovery: code %d, want 200", code)
	}
}

func grant(t *testing.T, c client.Client, user string, rule rbacv1.PolicyRule) {
	t.Helper()
	ctx := context.Background()
	if err := c.Create(ctx, &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: user, Namespace: "default"},
		Rules:      []rbacv1.PolicyRule{rule},
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: user, Namespace: "default"},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: user},
		Subjects:   []rbacv1.Subject{{APIGroup: rbacv1.GroupName, Kind: "User", Name: user}},
	}); err != nil {
		t.Fatal(err)
	}
}

func addUser(t *testing.T, env *envtest.Environment, name string) *rest.Config {
	t.Helper()
	u, err := env.AddUser(envtest.User{Name: name, Groups: []string{"system:authenticated"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return u.Config()
}

func startAPIServer(t *testing.T, env *envtest.Environment, admin *rest.Config, c client.Client) string {
	t.Helper()
	dir := t.TempDir()
	kubeconfig := filepath.Join(dir, "kubeconfig")
	writeKubeconfig(t, admin, kubeconfig)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	o := NewOptions()
	o.SecureServing.Listener = l
	o.SecureServing.ServerCert.CertDirectory = dir
	o.Authentication.RemoteKubeConfigFile = kubeconfig
	o.Authentication.ClientCert.ClientCA = filepath.Join(env.ControlPlane.APIServer.CertDir, "client-cert-auth-ca.crt")
	o.Authentication.TolerateInClusterLookupFailure = true
	o.Authorization.RemoteKubeConfigFile = kubeconfig
	// The authorizer caches decisions; keep them short so RBAC changes apply immediately.
	o.Authorization.AllowCacheTTL = 0
	o.Authorization.DenyCacheTTL = 0

	server, err := o.NewServer(NewHandler(c, DaemonConfig{Selector: labels.Everything()}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := Run(ctx, server); err != nil {
			t.Error(err)
		}
	}()
	t.Cleanup(func() { cancel(); <-done })

	addr := l.Addr().String()
	waitReady(t, "https://"+addr+"/readyz")
	return addr
}

func writeKubeconfig(t *testing.T, cfg *rest.Config, path string) {
	t.Helper()
	kc := clientcmdapi.NewConfig()
	kc.Clusters["envtest"] = &clientcmdapi.Cluster{Server: cfg.Host, CertificateAuthorityData: cfg.CAData}
	kc.AuthInfos["admin"] = &clientcmdapi.AuthInfo{ClientCertificateData: cfg.CertData, ClientKeyData: cfg.KeyData}
	kc.Contexts["envtest"] = &clientcmdapi.Context{Cluster: "envtest", AuthInfo: "admin"}
	kc.CurrentContext = "envtest"
	if err := clientcmd.WriteToFile(*kc, path); err != nil {
		t.Fatal(err)
	}
}

// do sends a request authenticated with cfg's client certificate. ch-vmm-gateway serves a
// self-signed certificate in this test, so server verification is skipped.
func do(t *testing.T, cfg *rest.Config, method, url string) int {
	t.Helper()
	cert, err := tls.X509KeyPair(cfg.CertData, cfg.KeyData)
	if err != nil {
		t.Fatal(err)
	}
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: true, //nolint:gosec // test server uses a self-signed certificate
	}}}
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func waitReady(t *testing.T, url string) {
	t.Helper()
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := hc.Get(url); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("%s not ready", url)
}
