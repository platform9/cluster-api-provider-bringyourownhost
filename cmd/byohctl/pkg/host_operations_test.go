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

// testHostname returns the hostname the client methods will look up. Seeding
// fake objects under this name is the simplest way to bridge the unmockable
// os.Hostname() call inside client methods.
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

// testSeams records observable side effects from the swappable dependencies.
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

func TestPerformHostOperation_KubeconfigMissing(t *testing.T) {
	// Point KubeconfigFilePath at a non-existent file. The other seams don't
	// matter because the check runs before them.
	origPath := service.KubeconfigFilePath
	service.KubeconfigFilePath = filepath.Join(t.TempDir(), "does-not-exist")
	t.Cleanup(func() { service.KubeconfigFilePath = origPath })

	for _, op := range []HostOperationType{OperationDeauthorise, OperationDecommission} {
		t.Run(string(op), func(t *testing.T) {
			err := PerformHostOperation(op, testNamespace, false)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "kubeconfig file not found")
		})
	}
}

func TestPerformHostOperation_ByoHostMissing_Deauthorise(t *testing.T) {
	t.Run("no force returns error", func(t *testing.T) {
		_, seams := installSeams(t) // no seeded ByoHost
		err := PerformHostOperation(OperationDeauthorise, testNamespace, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Cannot proceed")
		assert.Equal(t, 0, seams.purgeCalls, "deauthorise must not purge")
	})

	t.Run("force treats as no-op", func(t *testing.T) {
		_, seams := installSeams(t)
		require.NoError(t, PerformHostOperation(OperationDeauthorise, testNamespace, true))
		assert.Equal(t, 0, seams.purgeCalls)
		assert.Equal(t, 0, seams.askCalls, "force must not prompt")
	})
}

func TestPerformHostOperation_ByoHostMissing_Decommission(t *testing.T) {
	t.Run("force purges without prompt", func(t *testing.T) {
		_, seams := installSeams(t)
		require.NoError(t, PerformHostOperation(OperationDecommission, testNamespace, true))
		assert.Equal(t, 1, seams.purgeCalls)
		assert.Equal(t, 0, seams.askCalls)
	})

	t.Run("no force + user declines skips purge", func(t *testing.T) {
		_, seams := installSeams(t)
		seams.askResp = false
		require.NoError(t, PerformHostOperation(OperationDecommission, testNamespace, false))
		assert.Equal(t, 0, seams.purgeCalls)
		assert.Equal(t, 1, seams.askCalls)
	})

	t.Run("no force + user confirms runs purge", func(t *testing.T) {
		_, seams := installSeams(t)
		seams.askResp = true
		require.NoError(t, PerformHostOperation(OperationDecommission, testNamespace, false))
		assert.Equal(t, 1, seams.purgeCalls)
		assert.Equal(t, 1, seams.askCalls)
	})
}

