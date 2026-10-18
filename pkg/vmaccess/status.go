package vmaccess

import (
	"encoding/json"
	"errors"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WriteStatus writes err as a metav1.Status, the error format kubectl and client-go understand.
// Errors that are not API errors are written as InternalError. ch-vmm-gateway relays daemon
// responses as-is, so both use it to keep errors typed for clients.
func WriteStatus(w http.ResponseWriter, err error) {
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
