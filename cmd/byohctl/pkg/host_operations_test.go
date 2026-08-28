package pkg

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	capiv1beta1 "sigs.k8s.io/cluster-api/api/v1beta1"

	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/client"
	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
)

const testNamespace = "byoh-ns"

func testHostname(t *testing.T) string {
	t.Helper()
	h, err := os.Hostname()
	require.NoError(t, err)
	return h
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, infrastructurev1beta1.AddToScheme(s))
	require.NoError(t, capiv1beta1.AddToScheme(s))
	return s
}

func newByoHost(t *testing.T, machineRef *corev1.ObjectReference) *infrastructurev1beta1.ByoHost {
	t.Helper()
	return &infrastructurev1beta1.ByoHost{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testHostname(t),
			Namespace: testNamespace,
		},
		Status: infrastructurev1beta1.ByoHostStatus{MachineRef: machineRef},
	}
}

func fakeClient(t *testing.T, objs ...runtime.Object) *client.Client {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClient(testScheme(t), objs...)
	return &client.Client{DynamicClient: dyn}
}

// stubIO is a HostIO stub with canned answers and call counters.
type stubIO struct {
	confirmResp bool
	confirmErr  error
	confirmN    int
	purgeErr    error
	purgeN      int
}

func (s *stubIO) Confirm(string) (bool, error) {
	s.confirmN++
	return s.confirmResp, s.confirmErr
}

func (s *stubIO) Purge() error {
	s.purgeN++
	return s.purgeErr
}

func TestPerformHostOperation(t *testing.T) {
	tests := []struct {
		name            string
		objs            []runtime.Object
		operation       HostOperationType
		force           bool
		confirmResp     bool
		wantErr         bool
		wantErrContains string
		wantPurgeCalls  int
		wantAskCalls    int
	}{
		{
			name:            "deauthorise / ByoHost missing / no force returns error",
			operation:       OperationDeauthorise,
			force:           false,
			wantErr:         true,
			wantErrContains: "Cannot proceed",
		},
		{
			name:      "deauthorise / ByoHost missing / force treats as no-op",
			operation: OperationDeauthorise,
			force:     true,
		},
		{
			name:           "decommission / ByoHost missing / force purges without prompt",
			operation:      OperationDecommission,
			force:          true,
			wantPurgeCalls: 1,
		},
		{
			name:         "decommission / ByoHost missing / no force + user declines skips purge",
			operation:    OperationDecommission,
			force:        false,
			confirmResp:  false,
			wantAskCalls: 1,
		},
		{
			name:           "decommission / ByoHost missing / no force + user confirms runs purge",
			operation:      OperationDecommission,
			force:          false,
			confirmResp:    true,
			wantPurgeCalls: 1,
			wantAskCalls:   1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k8sClient := fakeClient(t, tt.objs...)
			io := &stubIO{confirmResp: tt.confirmResp}

			err := PerformHostOperation(t.Context(), k8sClient, io, tt.operation, testNamespace, tt.force)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrContains)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantPurgeCalls, io.purgeN, "purge calls mismatch")
			assert.Equal(t, tt.wantAskCalls, io.confirmN, "confirm calls mismatch")
		})
	}
}
