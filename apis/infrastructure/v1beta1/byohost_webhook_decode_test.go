// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	v1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func newTestByoHostValidator(t *testing.T) *ByoHostValidator {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, AddToScheme(scheme))
	decoder, err := admission.NewDecoder(scheme)
	require.NoError(t, err)
	return &ByoHostValidator{
		Client:  fake.NewClientBuilder().WithScheme(scheme).Build(),
		decoder: decoder,
	}
}

func TestHandleCreateUpdate_EmptyRawExtension(t *testing.T) {
	v := newTestByoHostValidator(t)

	resp := v.Handle(context.TODO(), admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Operation: admissionv1.Create,
		UserInfo:  v1.UserInfo{Username: byohHostOneUser},
		Object:    runtime.RawExtension{}, // no Raw bytes
	}})

	assert.False(t, resp.Allowed)
	require.NotNil(t, resp.Result)
	assert.EqualValues(t, http.StatusBadRequest, resp.Result.Code)
	assert.Equal(t, "there is no content to decode", resp.Result.Message)
}

func TestHandleCreateUpdate_MalformedRawExtension(t *testing.T) {
	v := newTestByoHostValidator(t)

	resp := v.Handle(context.TODO(), admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Operation: admissionv1.Create,
		UserInfo:  v1.UserInfo{Username: byohHostOneUser},
		Object: runtime.RawExtension{
			Raw: []byte(`{"metadata": this is not valid json`),
		},
	}})

	assert.False(t, resp.Allowed)
	require.NotNil(t, resp.Result)
	assert.EqualValues(t, http.StatusBadRequest, resp.Result.Code)
}

func TestHandleCreateUpdate_DecodesFromRawOnly(t *testing.T) {
	// The decoder reads req.Object.Raw exclusively; a pre-populated Object field
	// (as a client might set when round-tripping in-process) must be ignored.
	v := newTestByoHostValidator(t)

	byoHost := &ByoHost{
		TypeMeta:   metav1.TypeMeta{Kind: testByoHostKind, APIVersion: testAPIVersion},
		ObjectMeta: metav1.ObjectMeta{Name: defaultHostName, Namespace: DefaultNamespace},
	}
	raw, err := json.Marshal(byoHost)
	require.NoError(t, err)

	resp := v.Handle(context.TODO(), admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Operation: admissionv1.Create,
		UserInfo:  v1.UserInfo{Username: byohHostOneUser},
		Object: runtime.RawExtension{
			Raw:    raw,
			Object: &ByoHost{ObjectMeta: metav1.ObjectMeta{Name: "different-host"}},
		},
	}})

	assert.True(t, resp.Allowed)
}

func TestHandleDelete_EmptyOldObjectRawExtension(t *testing.T) {
	v := newTestByoHostValidator(t)

	resp := v.Handle(context.TODO(), admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Operation: admissionv1.Delete,
		OldObject: runtime.RawExtension{},
	}})

	assert.False(t, resp.Allowed)
	require.NotNil(t, resp.Result)
	assert.EqualValues(t, http.StatusBadRequest, resp.Result.Code)
	assert.Equal(t, "there is no content to decode", resp.Result.Message)
}
