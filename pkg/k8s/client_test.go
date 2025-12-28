package k8s_test

import (
	"path/filepath"
	"testing"

	"kuack-node/pkg/k8s"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const validPEM = `-----BEGIN CERTIFICATE-----
MIIC/zCCAeegAwIBAgIUBvU4MUEMSoPb+s4mAw09WMO0DIcwDQYJKoZIhvcNAQEL
BQAwDzENMAsGA1UEAwwEdGVzdDAeFw0yNTEyMjcyMzEzMjVaFw0yNjEyMjcyMzEz
MjVaMA8xDTALBgNVBAMMBHRlc3QwggEiMA0GCSqGSIb3DQEBAQUAA4IBDwAwggEK
AoIBAQDHGXIrvKjef4zvRxL81QNfSq2FlutAwG5ouJtR+MPIhVxI8QcHx/493kDp
yFretGMV8ieafMcDqH7NcJ9sAsKsBtItluv8wA4ZAifND59TeWELVb0VsYLLlsY0
69dAZ54iGiodJ/6unxcyxJOxu4eSvTtEb945lTK6wiIEUAZ52QkN7drhdLSMraXU
UX9JuYprwib54Smpqk5JtZ62cI1nxX/b///tmc5XxOBzPsVDza/GDKgR61d6LNJZ
v+aQNF45+O+XjRgz0wi+bxdBHfSBEombCjxlSIgkwJmuE28R8FG07LWZJBCDMXk8
Gl3qvGzISyOsVlBLcLFrypasUiYhAgMBAAGjUzBRMB0GA1UdDgQWBBQXHRVGJxfG
dR50lRoP4GXPiVhFdTAfBgNVHSMEGDAWgBQXHRVGJxfGdR50lRoP4GXPiVhFdTAP
BgNVHRMBAf8EBTADAQH/MA0GCSqGSIb3DQEBCwUAA4IBAQCMZIvTdmJtEpe3MPH2
X45yq7nUIA2NdPZiHJmbGW1TuHBnuIT8S9BiXEijwa/cILsbeP+/h8E1nI6SQuXk
Q7jJE50AHN9z1C87xYkToWrD/ftl5hQk68qp6TlKY47h/JXknIWpWJGbOzPZQ5NZ
8hmmCsyUQy5BhfVYgvQrskcKKgxsHx6k+9YYNdaKD4OFCX5YdAVyOytuX92V5Bma
GWLo5+fFCKspdvVCHXAwAqIl7jElb+U5frWYTykoHyhtCHRm7j6lFDm7ZT8faaKW
E6hJt8GGOqjT4rJ6eAOS2XWDPAgTyogE5ynyj2+//rBGhKH2MlWVjWZfZwEk5XS+
YltH
-----END CERTIFICATE-----`

func TestGetKubeClient(t *testing.T) {
	t.Parallel()

	// Create a temporary kubeconfig file
	tmpDir := t.TempDir()
	kubeconfigPath := filepath.Join(tmpDir, "kubeconfig")

	config := clientcmdapi.NewConfig()
	config.Clusters["test-cluster"] = &clientcmdapi.Cluster{
		Server:                   "https://localhost:6443",
		CertificateAuthorityData: []byte(validPEM),
	}
	config.Contexts["test-context"] = &clientcmdapi.Context{
		Cluster: "test-cluster",
	}
	config.CurrentContext = "test-context"

	err := clientcmd.WriteToFile(*config, kubeconfigPath)
	require.NoError(t, err)

	client, err := k8s.GetKubeClient(kubeconfigPath)
	require.NoError(t, err)
	assert.NotNil(t, client)
}

func TestGetKubeClient_Error(t *testing.T) {
	t.Parallel()
	// Pass invalid path
	_, err := k8s.GetKubeClient("/nonexistent/kubeconfig")
	require.Error(t, err)
}
