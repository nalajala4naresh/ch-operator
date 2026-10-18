package apiserver

import (
	"context"
	"fmt"
	"net"
	"net/http"

	"github.com/spf13/pflag"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/httpstream/wsstream"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"
	genericfilters "k8s.io/apiserver/pkg/server/filters"
	genericoptions "k8s.io/apiserver/pkg/server/options"
	"k8s.io/apiserver/pkg/util/compatibility"

	"github.com/nalajala4naresh/ch-operator/pkg/vmaccess"
)

var vmResource = schema.GroupResource{Group: "cloudhypervisor.quill.today", Resource: "virtualmachines"}

// Options are the command-line options of ch-vmm-gateway.
type Options struct {
	SecureServing  *genericoptions.SecureServingOptionsWithLoopback
	Authentication *genericoptions.DelegatingAuthenticationOptions
	Authorization  *genericoptions.DelegatingAuthorizationOptions
	Audit          *genericoptions.AuditOptions
}

// NewOptions returns options with defaults for running in-cluster behind the aggregation layer.
func NewOptions() *Options {
	o := &Options{
		SecureServing:  genericoptions.NewSecureServingOptions().WithLoopback(),
		Authentication: genericoptions.NewDelegatingAuthenticationOptions(),
		Authorization:  genericoptions.NewDelegatingAuthorizationOptions(),
		Audit:          genericoptions.NewAuditOptions(),
	}
	o.SecureServing.BindPort = 8443
	o.SecureServing.ServerCert.CertDirectory = "/var/lib/ch-vmm-gateway/serving-cert"
	o.SecureServing.ServerCert.PairName = "tls"
	// Health probes come from the kubelet without credentials.
	o.Authorization.AlwaysAllowPaths = []string{"/healthz", "/readyz", "/livez"}
	return o
}

// AddFlags registers the options' flags.
func (o *Options) AddFlags(fs *pflag.FlagSet) {
	o.SecureServing.AddFlags(fs)
	o.Authentication.AddFlags(fs)
	o.Authorization.AddFlags(fs)
	o.Audit.AddFlags(fs)
}

// NewServer builds the generic API server and installs handler under /apis/<group>.
func (o *Options) NewServer(handler http.Handler) (*genericapiserver.GenericAPIServer, error) {
	if err := o.SecureServing.MaybeDefaultWithSelfSignedCerts("localhost", nil, []net.IP{net.ParseIP("127.0.0.1")}); err != nil {
		return nil, fmt.Errorf("create self-signed certificates: %w", err)
	}

	scheme := runtime.NewScheme()
	metav1.AddToGroupVersion(scheme, schema.GroupVersion{Version: "v1"})
	codecs := serializer.NewCodecFactory(scheme)

	config := genericapiserver.NewConfig(codecs)
	config.EffectiveVersion = compatibility.DefaultBuildEffectiveVersion()
	if err := o.SecureServing.ApplyTo(&config.SecureServing, &config.LoopbackClientConfig); err != nil {
		return nil, err
	}
	if err := o.Authentication.ApplyTo(&config.Authentication, config.SecureServing, nil); err != nil {
		return nil, err
	}
	if err := o.Authorization.ApplyTo(&config.Authorization); err != nil {
		return nil, err
	}
	if err := o.Audit.ApplyTo(config); err != nil {
		return nil, err
	}

	// Console and port-forward streams last as long as the session; exempt them from the
	// request timeout and in-flight limits.
	basic := genericfilters.BasicLongRunningRequestCheck(sets.NewString("watch"), sets.NewString("proxy"))
	config.LongRunningFunc = func(r *http.Request, ri *request.RequestInfo) bool {
		if ri != nil && ri.APIGroup == vmaccess.GroupName && vmaccess.StreamingSubresources[ri.Subresource] {
			return true
		}
		return wsstream.IsWebSocketRequest(r) || basic(r, ri)
	}

	config.RequestInfoResolver = streamingVerbResolver{genericapiserver.NewRequestInfoResolver(config)}

	server, err := config.Complete(nil).New("ch-vmm-gateway", genericapiserver.NewEmptyDelegate())
	if err != nil {
		return nil, err
	}

	server.Handler.NonGoRestfulMux.Handle("/apis/"+vmaccess.GroupName, handler)
	server.Handler.NonGoRestfulMux.HandlePrefix("/apis/"+vmaccess.GroupName+"/", handler)
	server.DiscoveryGroupManager.AddGroup(apiGroup())
	if server.AggregatedDiscoveryGroupManager != nil {
		server.AggregatedDiscoveryGroupManager.AddGroupVersion(vmaccess.GroupName, aggregatedDiscovery())
	}
	return server, nil
}

// streamingVerbResolver authorizes streaming subresources with the "create" verb even though the
// WebSocket handshake is a GET. A stream is interactive write access to the VM, and "get" is
// commonly granted by read-only roles. Core Kubernetes does the same for pods/exec, attach and
// portforward (AuthorizePodWebsocketUpgradeCreatePermission, kubernetes/kubernetes#133515).
type streamingVerbResolver struct {
	request.RequestInfoResolver
}

func (r streamingVerbResolver) NewRequestInfo(req *http.Request) (*request.RequestInfo, error) {
	info, err := r.RequestInfoResolver.NewRequestInfo(req)
	if err != nil || info == nil {
		return info, err
	}
	if info.IsResourceRequest && info.APIGroup == vmaccess.GroupName && vmaccess.StreamingSubresources[info.Subresource] {
		info.Verb = "create"
	}
	return info, nil
}

// Run serves until ctx is cancelled.
func Run(ctx context.Context, server *genericapiserver.GenericAPIServer) error {
	return server.PrepareRun().RunWithContext(ctx)
}
