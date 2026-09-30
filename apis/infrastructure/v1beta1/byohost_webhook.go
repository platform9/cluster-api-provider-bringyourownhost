// Copyright 2021 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	v1 "k8s.io/api/admission/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

//+kubebuilder:webhook:path=/validate-infrastructure-cluster-x-k8s-io-v1beta1-byohost,mutating=false,failurePolicy=fail,sideEffects=None,groups=infrastructure.cluster.x-k8s.io,resources=byohosts,verbs=create;update;delete,versions=v1beta1,name=vbyohost.kb.io,admissionReviewVersions={v1,v1beta1}

// +k8s:deepcopy-gen=false
// ByoHostValidator validates ByoHosts
type ByoHostValidator struct {
	Client  client.Client
	Decoder admission.Decoder
}

// The byoh-controller-manager's namespace differs by deployment: "byoh-system" is the OSS
// default (config/default, e2e), "kaapi" is the PF9 production deployment. Both identities
// are allowlisted to bypass the per-agent host-ownership check below.
const (
	kaapiManagerServiceAccount      = "system:serviceaccount:kaapi:byoh-controller-manager"
	byohSystemManagerServiceAccount = "system:serviceaccount:byoh-system:byoh-controller-manager"
)

var managerServiceAccounts = map[string]struct{}{
	kaapiManagerServiceAccount:      struct{}{},
	byohSystemManagerServiceAccount: struct{}{},
}

// Precompile email-like regex for efficiency
var emailLikeUserRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)

// nolint: gocritic
// Handle handles all the requests for ByoHost resource
func (v *ByoHostValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	var response admission.Response

	switch req.Operation {
	case v1.Create, v1.Update:
		response = v.handleCreateUpdate(&req)
	case v1.Delete:
		response = v.handleDelete(ctx, &req)
	default:
		response = admission.Allowed("")
	}
	return response
}

func (v *ByoHostValidator) handleCreateUpdate(req *admission.Request) admission.Response {
	byoHost := &ByoHost{}
	err := v.Decoder.Decode(*req, byoHost)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	userName := req.UserInfo.Username
	// allow manager service account to patch ByoHost
	if _, ok := managerServiceAccounts[userName]; ok {
		return admission.Allowed("")
	}

	// allow users with email-like usernames
	if emailLikeUserRegex.MatchString(userName) {
		return admission.Allowed("")
	}

	// The host name comes from the authenticated identity, not from the
	// object that issued the certificate, which may be gone by now.
	hostName, err := HostNameFromIdentity(userName)
	if err != nil {
		return admission.Denied(fmt.Sprintf("%s is not a valid agent username: %s", userName, err.Error()))
	}

	if hostName != byoHost.Name {
		return admission.Denied(fmt.Sprintf("%s cannot create/update resource %s", userName, byoHost.Name))
	}

	// On create the object must already carry the requester's identity, which
	// the stamping webhook wrote. A mismatch means the stamp was bypassed.
	if req.Operation == v1.Create && byoHost.Spec.Identity != userName {
		return admission.Denied(fmt.Sprintf("%s cannot create resource %s with identity %q", userName, byoHost.Name, byoHost.Spec.Identity))
	}

	return admission.Allowed("")
}

func (v *ByoHostValidator) handleDelete(ctx context.Context, req *admission.Request) admission.Response {
	byoHost := &ByoHost{}
	err := v.Decoder.DecodeRaw(req.OldObject, byoHost)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if byoHost.Status.MachineRef != nil {
		// allow webhook to delete ByoHost when MachineRef is assigned but respective byoMachine doesn't exist
		byoMachine := byoHost.Status.MachineRef.Name

		// Fetch the ByoMachine instance
		byoMachineObj := &ByoMachine{}
		err = v.Client.Get(ctx, client.ObjectKey{
			Name:      byoMachine,
			Namespace: byoHost.Namespace,
		}, byoMachineObj)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return admission.Allowed("")
			}
			return admission.Errored(http.StatusInternalServerError, err)
		}

		return admission.Denied("cannot delete ByoHost when MachineRef is assigned")
	}
	return admission.Allowed("")
}

// hostIdentityPrefix is what every host certificate's common name starts
// with. The two segments after it are the host name and the suffix of the
// BootstrapKubeconfig that issued the certificate.
const hostIdentityPrefix = "byoh:host:"

// HostNameFromIdentity returns the host name a certificate identity names.
// Segments are compared whole, because a substring match would let a
// certificate for "worker-1" act on "worker-10".
//
// For example, "byoh:host:coke-worker-1:x7k2p" returns "coke-worker-1".
func HostNameFromIdentity(identity string) (string, error) {
	if !strings.HasPrefix(identity, hostIdentityPrefix) {
		return "", fmt.Errorf("identity %q does not start with %q", identity, hostIdentityPrefix)
	}

	segments := strings.Split(identity, ":")
	if len(segments) != 4 { //nolint: mnd
		return "", fmt.Errorf("identity %q is not of the form %s<hostName>:<suffix>", identity, hostIdentityPrefix)
	}

	hostName := segments[2]
	if hostName == "" {
		return "", fmt.Errorf("identity %q carries an empty host name", identity)
	}

	if segments[3] == "" {
		return "", fmt.Errorf("identity %q carries an empty suffix", identity)
	}

	return hostName, nil
}

//+kubebuilder:webhook:path=/mutate-infrastructure-cluster-x-k8s-io-v1beta1-byohost,mutating=true,failurePolicy=fail,sideEffects=None,groups=infrastructure.cluster.x-k8s.io,resources=byohosts,verbs=create,versions=v1beta1,name=mbyohost.kb.io,admissionReviewVersions=v1

// +k8s:deepcopy-gen=false
// ByoHostIdentityStamper records which certificate identity may act as a host,
// taken from whoever authenticated the creating request. Renewal is authorized
// against that record, so it must come from the request and never from the
// object body.
//
// It is a spec field rather than a status one because the API server drops the
// status stanza on create, so a webhook cannot stamp status at creation time.
type ByoHostIdentityStamper struct {
	Decoder admission.Decoder
}

// Handle implements admission.Handler.
// FIXME CLAUDE: Explain this nolint. Why do we need it?
// nolint: gocritic // admission.Handler fixes this signature.
func (s *ByoHostIdentityStamper) Handle(_ context.Context, req admission.Request) admission.Response {
	if req.Operation != v1.Create {
		return admission.Allowed("")
	}

	byoHost := &ByoHost{}
	if err := s.Decoder.Decode(req, byoHost); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	// Only a host certificate gets stamped. An operator or the manager creates
	// hosts under its own identity, which names no host and would make the
	// record meaningless.
	if _, err := HostNameFromIdentity(req.UserInfo.Username); err != nil {
		return admission.Allowed("")
	}

	byoHost.Spec.Identity = req.UserInfo.Username

	marshaled, err := json.Marshal(byoHost)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}

	return admission.PatchResponseFromRaw(req.Object.Raw, marshaled)
}
