// Package apiserver implements ch-vmm-gateway, an aggregated API server that serves VirtualMachine
// subresources (console, portforward, pause, unpause, restart, migrate) under
// subresources.cloudhypervisor.quill.today. kube-apiserver authenticates callers; the generic
// API server filters authorize every request with a SubjectAccessReview before it reaches these
// handlers, so RBAC on "virtualmachines/<subresource>" controls who can do what.
package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"

	v1beta1 "github.com/nalajala4naresh/chvmm-api/v1beta1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nalajala4naresh/ch-operator/pkg/vmaccess"
	"github.com/nalajala4naresh/ch-operator/pkg/vmactions"
)

// DaemonConfig says how to reach the ch-vmm-daemon on a VM's node.
type DaemonConfig struct {
	Namespace  string
	Selector   labels.Selector
	Port       int
	ServerName string
	Transport  http.RoundTripper
}

// Handler serves the subresource API.
type Handler struct {
	Client client.Client
	Daemon DaemonConfig

	mux *http.ServeMux
}

const vmPath = "/apis/" + vmaccess.GroupName + "/" + vmaccess.Version + "/namespaces/{namespace}/virtualmachines/{name}/"

// NewHandler returns a Handler for paths under /apis/<group>.
func NewHandler(c client.Client, daemon DaemonConfig) *Handler {
	h := &Handler{Client: c, Daemon: daemon, mux: http.NewServeMux()}

	h.mux.HandleFunc("GET /apis/"+vmaccess.GroupName, h.writeJSON(apiGroup()))
	h.mux.HandleFunc("GET /apis/"+vmaccess.GroupName+"/"+vmaccess.Version, h.writeJSON(apiResourceList()))

	h.mux.HandleFunc("GET "+vmPath+vmaccess.SubresourceConsole, h.console)
	h.mux.HandleFunc("GET "+vmPath+vmaccess.SubresourcePortForward+"/{port}", h.portForward)

	h.mux.HandleFunc("POST "+vmPath+vmaccess.SubresourcePause, h.action(vmactions.Pause))
	h.mux.HandleFunc("POST "+vmPath+vmaccess.SubresourceUnpause, h.action(vmactions.Unpause))
	h.mux.HandleFunc("POST "+vmPath+vmaccess.SubresourceRestart, h.action(vmactions.Restart))
	h.mux.HandleFunc("POST "+vmPath+vmaccess.SubresourceMigrate, h.migrate)
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) console(w http.ResponseWriter, r *http.Request) {
	vm, ok := h.getRunningVM(w, r)
	if !ok {
		return
	}
	h.proxyToDaemon(w, r, vm, vmaccess.DaemonConsolePath(vm.Namespace, vm.Name))
}

func (h *Handler) portForward(w http.ResponseWriter, r *http.Request) {
	port, err := strconv.Atoi(r.PathValue("port"))
	if err != nil || port < 1 || port > 65535 {
		writeStatus(w, apierrors.NewBadRequest(fmt.Sprintf("invalid port %q", r.PathValue("port"))))
		return
	}
	vm, ok := h.getRunningVM(w, r)
	if !ok {
		return
	}
	h.proxyToDaemon(w, r, vm, vmaccess.DaemonPortForwardPath(vm.Namespace, vm.Name, port))
}

func (h *Handler) action(do func(context.Context, client.Client, types.NamespacedName) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := do(r.Context(), h.Client, vmKey(r)); err != nil {
			writeStatus(w, err)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}
}

func (h *Handler) migrate(w http.ResponseWriter, r *http.Request) {
	migration, err := vmactions.Migrate(r.Context(), h.Client, vmKey(r))
	if err != nil {
		writeStatus(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(migration)
}

// proxyToDaemon forwards the (WebSocket upgrade) request to the daemon on the VM's node.
// httputil.ReverseProxy splices the upgraded connection in both directions.
func (h *Handler) proxyToDaemon(w http.ResponseWriter, r *http.Request, vm *v1beta1.VirtualMachine, path string) {
	podIP, err := h.daemonIP(r.Context(), vm.Status.NodeName)
	if err != nil {
		writeStatus(w, err)
		return
	}
	user := ""
	if u, ok := request.UserFrom(r.Context()); ok {
		user = u.GetName()
	}

	target := &url.URL{Scheme: "https", Host: fmt.Sprintf("%s:%d", podIP, h.Daemon.Port), Path: path}
	proxy := &httputil.ReverseProxy{
		Transport: h.Daemon.Transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL = target
			pr.Out.Host = h.Daemon.ServerName
			// Never forward the caller's credentials or identity headers to the daemon.
			for name := range pr.Out.Header {
				lower := strings.ToLower(name)
				if lower == "authorization" || strings.HasPrefix(lower, "impersonate-") || strings.HasPrefix(lower, "x-remote-") {
					pr.Out.Header.Del(name)
				}
			}
			pr.Out.Header.Set(vmaccess.UserHeader, user)
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			klog.ErrorS(err, "proxy to daemon", "vm", klog.KObj(vm), "node", vm.Status.NodeName)
			writeStatus(w, apierrors.NewServiceUnavailable(fmt.Sprintf("connect to VM on node %s: %v", vm.Status.NodeName, err)))
		},
	}
	klog.InfoS("streaming to VM", "vm", klog.KObj(vm), "path", path, "user", user)
	proxy.ServeHTTP(w, r)
}

func (h *Handler) daemonIP(ctx context.Context, nodeName string) (string, error) {
	var pods corev1.PodList
	if err := h.Client.List(ctx, &pods,
		client.InNamespace(h.Daemon.Namespace),
		client.MatchingLabelsSelector{Selector: h.Daemon.Selector},
		client.MatchingFields{"spec.nodeName": nodeName},
	); err != nil {
		return "", fmt.Errorf("list daemon pods: %w", err)
	}
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning && pod.Status.PodIP != "" && pod.DeletionTimestamp == nil {
			return pod.Status.PodIP, nil
		}
	}
	return "", apierrors.NewServiceUnavailable(fmt.Sprintf("no running ch-vmm-daemon on node %s", nodeName))
}

func (h *Handler) getRunningVM(w http.ResponseWriter, r *http.Request) (*v1beta1.VirtualMachine, bool) {
	var vm v1beta1.VirtualMachine
	if err := h.Client.Get(r.Context(), vmKey(r), &vm); err != nil {
		writeStatus(w, err)
		return nil, false
	}
	switch vm.Status.Phase {
	case v1beta1.VirtualMachineRunning, v1beta1.VirtualMachinePaused, v1beta1.VirtualMachineResumed:
	default:
		writeStatus(w, apierrors.NewConflict(vmResource, vm.Name,
			fmt.Errorf("VM is not running, current phase is %q", vm.Status.Phase)))
		return nil, false
	}
	if vm.Status.NodeName == "" {
		writeStatus(w, apierrors.NewConflict(vmResource, vm.Name, errors.New("VM is not scheduled to a node")))
		return nil, false
	}
	return &vm, true
}

func (h *Handler) writeJSON(v any) http.HandlerFunc {
	body, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}
}

func vmKey(r *http.Request) types.NamespacedName {
	return types.NamespacedName{Namespace: r.PathValue("namespace"), Name: r.PathValue("name")}
}

// writeStatus writes err as a metav1.Status, the error format kubectl and client-go understand.
func writeStatus(w http.ResponseWriter, err error) {
	var status metav1.Status
	var apiStatus apierrors.APIStatus
	if errors.As(err, &apiStatus) {
		status = apiStatus.Status()
	} else {
		status = apierrors.NewInternalError(err).Status()
	}
	status.TypeMeta = metav1.TypeMeta{Kind: "Status", APIVersion: "v1"}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(int(status.Code))
	_ = json.NewEncoder(w).Encode(status)
}
