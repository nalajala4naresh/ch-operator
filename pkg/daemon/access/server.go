// Package access serves streaming access to the VMs on this node (serial console, TCP port
// forwarding) to ch-vmm-gateway over mTLS. Authorization of the end user happens in ch-vmm-gateway;
// this server only trusts ch-vmm-gateway's client certificate.
package access

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nalajala4naresh/chvmm-api/subresources"
	v1beta1 "github.com/nalajala4naresh/chvmm-api/v1beta1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nalajala4naresh/ch-operator/pkg/vmaccess"
)

const dialTimeout = 5 * time.Second

var vmResource = v1beta1.Resource("virtualmachines")

// Server is a manager.Runnable serving the daemon's streaming endpoints.
type Server struct {
	Client    client.Client
	NodeName  string
	Addr      string
	TLSConfig *tls.Config
	// SerialSocketPath returns the serial socket of a VM pod. Defaults to vmaccess.HostSerialSocketPath.
	SerialSocketPath func(vmPodUID types.UID) string

	upgrader websocket.Upgrader
}

func (s *Server) handler() http.Handler {
	if s.SerialSocketPath == nil {
		s.SerialSocketPath = vmaccess.HostSerialSocketPath
	}
	s.upgrader = websocket.Upgrader{
		Subprotocols:    []string{subresources.PlainStreamProtocol},
		ReadBufferSize:  32 * 1024,
		WriteBufferSize: 32 * 1024,
		// Only ch-vmm-gateway can connect (mTLS); browsers never reach this endpoint directly.
		CheckOrigin: func(*http.Request) bool { return true },
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/namespaces/{namespace}/virtualmachines/{name}/console", s.console)
	mux.HandleFunc("GET /v1/namespaces/{namespace}/virtualmachines/{name}/portforward/{port}", s.portForward)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

// Handler returns the HTTP handler; Start serves it with TLS.
func (s *Server) Handler() http.Handler {
	return s.handler()
}

// Start implements manager.Runnable.
func (s *Server) Start(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.Addr,
		Handler:           s.handler(),
		TLSConfig:         s.TLSConfig,
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServeTLS("", "") }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

func (s *Server) console(w http.ResponseWriter, r *http.Request) {
	vm, ok := s.getLocalVM(w, r)
	if !ok {
		return
	}
	if vm.Status.VMPodUID == "" {
		vmaccess.WriteStatus(w, apierrors.NewConflict(vmResource, vm.Name, errors.New("VM has no running pod")))
		return
	}

	conn, err := net.DialTimeout("unix", s.SerialSocketPath(vm.Status.VMPodUID), dialTimeout)
	if err != nil {
		// VMs started before serial socket support use a TTY console until they restart.
		vmaccess.WriteStatus(w, apierrors.NewConflict(vmResource, vm.Name, fmt.Errorf("serial console is not available: %w", err)))
		return
	}
	s.serve(w, r, vm, "console", conn)
}

func (s *Server) portForward(w http.ResponseWriter, r *http.Request) {
	port, err := strconv.Atoi(r.PathValue("port"))
	if err != nil || port < 1 || port > 65535 {
		vmaccess.WriteStatus(w, apierrors.NewBadRequest(fmt.Sprintf("invalid port %q", r.PathValue("port"))))
		return
	}
	vm, ok := s.getLocalVM(w, r)
	if !ok {
		return
	}

	var pod corev1.Pod
	if err := s.Client.Get(r.Context(), types.NamespacedName{Namespace: vm.Namespace, Name: vm.Status.VMPodName}, &pod); err != nil {
		vmaccess.WriteStatus(w, apierrors.NewConflict(vmResource, vm.Name, fmt.Errorf("get VM pod: %w", err)))
		return
	}
	if pod.Status.PodIP == "" {
		vmaccess.WriteStatus(w, apierrors.NewConflict(vmResource, vm.Name, errors.New("VM pod has no IP")))
		return
	}

	// The guest owns the pod IP (bridge) or receives everything sent to it (masquerade), so the
	// pod IP reaches the guest for both network modes.
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(pod.Status.PodIP, strconv.Itoa(port)), dialTimeout)
	if err != nil {
		vmaccess.WriteStatus(w, apierrors.NewServiceUnavailable(fmt.Sprintf("connect to VM port %d: %v", port, err)))
		return
	}
	s.serve(w, r, vm, "portforward/"+strconv.Itoa(port), conn)
}

// serve upgrades the request and splices it to conn. conn is opened before the upgrade so
// connection failures are reported as HTTP errors rather than as a closed stream.
func (s *Server) serve(w http.ResponseWriter, r *http.Request, vm *v1beta1.VirtualMachine, what string, conn net.Conn) {
	log := ctrl.Log.WithName("access").WithValues("vm", client.ObjectKeyFromObject(vm), "stream", what,
		"user", r.Header.Get(vmaccess.UserHeader))

	ws, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		conn.Close()
		log.Error(err, "upgrade to WebSocket")
		return
	}
	log.Info("stream opened")
	if err := vmaccess.Splice(ws, conn); err != nil {
		log.Info("stream closed with error", "error", err.Error())
		return
	}
	log.Info("stream closed")
}

func (s *Server) getLocalVM(w http.ResponseWriter, r *http.Request) (*v1beta1.VirtualMachine, bool) {
	key := types.NamespacedName{Namespace: r.PathValue("namespace"), Name: r.PathValue("name")}
	var vm v1beta1.VirtualMachine
	if err := s.Client.Get(r.Context(), key, &vm); err != nil {
		vmaccess.WriteStatus(w, err)
		return nil, false
	}
	if vm.Status.NodeName != s.NodeName {
		vmaccess.WriteStatus(w, apierrors.NewConflict(vmResource, vm.Name, fmt.Errorf("VM is not running on node %s", s.NodeName)))
		return nil, false
	}
	return &vm, true
}
