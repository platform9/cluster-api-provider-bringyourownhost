// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"context"
	"encoding/json"
	"net/http"

	v1 "k8s.io/api/admission/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// FIXME CLAUDE: Move this file's contents into byohost_webhook.go. Similarly for the tests.

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
