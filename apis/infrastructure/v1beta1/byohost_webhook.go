// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"context"
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
	decoder *admission.Decoder
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
	err := v.decoder.Decode(*req, byoHost)
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

	substrs := strings.Split(userName, ":")

	if len(substrs) < 2 { //nolint: mnd
		return admission.Denied(fmt.Sprintf("%s is not a valid agent username", userName))
	}

	// An agent's username encodes the host it owns as the third colon-separated segment
	// (format: byoh:host:<hostname>). Reject requests where the encoded host does not
	// match the target ByoHost — an agent must not create or update another agent's host.
	if len(substrs) >= 3 && !strings.Contains(byoHost.Name, substrs[2]) {
		return admission.Denied(fmt.Sprintf("%s cannot create/update resource %s", userName, byoHost.Name))
	}

	return admission.Allowed("")
}

func (v *ByoHostValidator) handleDelete(ctx context.Context, req *admission.Request) admission.Response {
	byoHost := &ByoHost{}
	err := v.decoder.DecodeRaw(req.OldObject, byoHost)
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
			return admission.Denied("cannot delete ByoHost when byomachine exists")
		}

		return admission.Denied("cannot delete ByoHost when MachineRef is assigned")
	}
	return admission.Allowed("")
}

// InjectDecoder injects the decoder.
func (v *ByoHostValidator) InjectDecoder(d *admission.Decoder) error {
	v.decoder = d
	return nil
}
