package pkg

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	capiv1beta1 "sigs.k8s.io/cluster-api/api/v1beta1"

	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/client"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/service"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/utils"
	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
)

const testNamespace = "byoh-ns"

var byoHostGVR = schema.GroupVersionResource{
	Group:    "infrastructure.cluster.x-k8s.io",
	Version:  "v1beta1",
	Resource: "byohosts",
}

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

type testSeams struct {
	purgeCalls int
	askResp    bool
	askErr     error
	askCalls   int
}

// installSeams wires a fake dynamic client, a temp kubeconfig, and stubs for
// PurgeDebianPackage / AskBool. All originals are restored via t.Cleanup.
func installSeams(t *testing.T, objs ...runtime.Object) (*client.Client, *testSeams) {
	t.Helper()

	dyn := dynamicfake.NewSimpleDynamicClient(testScheme(t), objs...)
	fakeClient := &client.Client{DynamicClient: dyn}

	kubeconfigPath := filepath.Join(t.TempDir(), "config")
	require.NoError(t, os.WriteFile(kubeconfigPath, []byte("apiVersion: v1\nkind: Config\n"), 0o600))
	origPath := service.KubeconfigFilePath
	service.KubeconfigFilePath = kubeconfigPath
	t.Cleanup(func() { service.KubeconfigFilePath = origPath })

	origGet := client.GetK8sClient
	client.GetK8sClient = func(_ string) (*client.Client, error) { return fakeClient, nil }
	t.Cleanup(func() { client.GetK8sClient = origGet })

	seams := &testSeams{}
	origAsk := utils.AskBool
	utils.AskBool = func(_ string, _ ...interface{}) (bool, error) {
		seams.askCalls++
		return seams.askResp, seams.askErr
	}
	t.Cleanup(func() { utils.AskBool = origAsk })

	origPurge := service.PurgeDebianPackage
	service.PurgeDebianPackage = func() error {
		seams.purgeCalls++
		return nil
	}
	t.Cleanup(func() { service.PurgeDebianPackage = origPurge })

	return fakeClient, seams
}

// pointKubeconfigAtMissingFile sets KubeconfigFilePath at a non-existent path
// so the kubeconfig existence check fails before any seam is exercised.
func pointKubeconfigAtMissingFile(t *testing.T) {
	t.Helper()
	origPath := service.KubeconfigFilePath
	service.KubeconfigFilePath = filepath.Join(t.TempDir(), "does-not-exist")
	t.Cleanup(func() { service.KubeconfigFilePath = origPath })
}

func TestPerformHostOperation(t *testing.T) {
	tests := []struct {
		name string
		// setup prepares the environment for the call. It returns the seams
		// struct when installSeams was used, or nil when the case exercises a
		// path that runs before the seams are consulted (e.g. missing kubeconfig).
		setup           func(t *testing.T) *testSeams
		operation       HostOperationType
		force           bool
		askResp         bool
		wantErr         bool
		wantErrContains string
		wantPurgeCalls  int
		wantAskCalls    int
	}{
		{
			name:            "kubeconfig missing / deauthorise",
			setup:           func(t *testing.T) *testSeams { pointKubeconfigAtMissingFile(t); return nil },
			operation:       OperationDeauthorise,
			wantErr:         true,
			wantErrContains: "kubeconfig file not found",
		},
		{
			name:            "kubeconfig missing / decommission",
			setup:           func(t *testing.T) *testSeams { pointKubeconfigAtMissingFile(t); return nil },
			operation:       OperationDecommission,
			wantErr:         true,
			wantErrContains: "kubeconfig file not found",
		},
		{
			name:            "deauthorise / ByoHost missing / no force returns error",
			setup:           func(t *testing.T) *testSeams { _, s := installSeams(t); return s },
			operation:       OperationDeauthorise,
			force:           false,
			wantErr:         true,
			wantErrContains: "Cannot proceed",
		},
		{
			name:      "deauthorise / ByoHost missing / force treats as no-op",
			setup:     func(t *testing.T) *testSeams { _, s := installSeams(t); return s },
			operation: OperationDeauthorise,
			force:     true,
		},
		{
			name:           "decommission / ByoHost missing / force purges without prompt",
			setup:          func(t *testing.T) *testSeams { _, s := installSeams(t); return s },
			operation:      OperationDecommission,
			force:          true,
			wantPurgeCalls: 1,
		},
		{
			name:         "decommission / ByoHost missing / no force + user declines skips purge",
			setup:        func(t *testing.T) *testSeams { _, s := installSeams(t); return s },
			operation:    OperationDecommission,
			force:        false,
			askResp:      false,
			wantAskCalls: 1,
		},
		{
			name:           "decommission / ByoHost missing / no force + user confirms runs purge",
			setup:          func(t *testing.T) *testSeams { _, s := installSeams(t); return s },
			operation:      OperationDecommission,
			force:          false,
			askResp:        true,
			wantPurgeCalls: 1,
			wantAskCalls:   1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seams := tt.setup(t)
			if seams != nil {
				seams.askResp = tt.askResp
			}

			err := PerformHostOperation(tt.operation, testNamespace, tt.force)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrContains)
			} else {
				require.NoError(t, err)
			}

			if seams != nil {
				assert.Equal(t, tt.wantPurgeCalls, seams.purgeCalls, "purgeCalls mismatch")
				assert.Equal(t, tt.wantAskCalls, seams.askCalls, "askCalls mismatch")
			}
		})
	}
}
