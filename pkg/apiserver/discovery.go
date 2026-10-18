package apiserver

import (
	apidiscoveryv2 "k8s.io/api/apidiscovery/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nalajala4naresh/ch-operator/pkg/vmaccess"
)

type subresource struct {
	name  string
	verbs []string
}

var subresources = []subresource{
	// Streams are opened with a WebSocket GET but authorized as "create", like pods/exec.
	{vmaccess.SubresourceConsole, []string{"create", "get"}},
	{vmaccess.SubresourcePortForward, []string{"create", "get"}},
	// Actions are POSTs (verb "create"), like pods/eviction.
	{vmaccess.SubresourcePause, []string{"create"}},
	{vmaccess.SubresourceUnpause, []string{"create"}},
	{vmaccess.SubresourceRestart, []string{"create"}},
	{vmaccess.SubresourceMigrate, []string{"create"}},
}

var groupVersion = metav1.GroupVersionForDiscovery{
	GroupVersion: vmaccess.GroupName + "/" + vmaccess.Version,
	Version:      vmaccess.Version,
}

func apiGroup() metav1.APIGroup {
	return metav1.APIGroup{
		TypeMeta:         metav1.TypeMeta{Kind: "APIGroup", APIVersion: "v1"},
		Name:             vmaccess.GroupName,
		Versions:         []metav1.GroupVersionForDiscovery{groupVersion},
		PreferredVersion: groupVersion,
	}
}

func apiResourceList() metav1.APIResourceList {
	list := metav1.APIResourceList{
		TypeMeta:     metav1.TypeMeta{Kind: "APIResourceList", APIVersion: "v1"},
		GroupVersion: groupVersion.GroupVersion,
	}
	for _, sr := range subresources {
		list.APIResources = append(list.APIResources, metav1.APIResource{
			Name:       "virtualmachines/" + sr.name,
			Namespaced: true,
			Kind:       "VirtualMachine",
			Verbs:      sr.verbs,
		})
	}
	return list
}

// aggregatedDiscovery describes the group version for the aggregated discovery document.
// The parent "virtualmachines" resource lives in cloudhypervisor.quill.today, so only its
// subresources are listed here.
func aggregatedDiscovery() apidiscoveryv2.APIVersionDiscovery {
	vd := apidiscoveryv2.APIVersionDiscovery{
		Version:   vmaccess.Version,
		Freshness: apidiscoveryv2.DiscoveryFreshnessCurrent,
	}
	resource := apidiscoveryv2.APIResourceDiscovery{
		Resource: "virtualmachines",
		Scope:    apidiscoveryv2.ScopeNamespace,
		ResponseKind: &metav1.GroupVersionKind{
			Group: "cloudhypervisor.quill.today", Version: vmaccess.Version, Kind: "VirtualMachine",
		},
	}
	for _, sr := range subresources {
		resource.Subresources = append(resource.Subresources, apidiscoveryv2.APISubresourceDiscovery{
			Subresource: sr.name,
			Verbs:       sr.verbs,
		})
	}
	vd.Resources = []apidiscoveryv2.APIResourceDiscovery{resource}
	return vd
}
