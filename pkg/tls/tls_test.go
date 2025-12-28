package tls_test

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"kuack-node/pkg/tls"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const testNodeIP = "127.0.0.1"

var errCreateFailed = errors.New("create failed")
var errGetFailed = errors.New("get failed")

func TestGenerateSelfSignedCert(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	certFile := filepath.Join(tmpDir, "cert.pem")
	keyFile := filepath.Join(tmpDir, "key.pem")

	err := tls.GenerateSelfSignedCert(testNodeIP, certFile, keyFile)
	require.NoError(t, err)

	assert.FileExists(t, certFile)
	assert.FileExists(t, keyFile)

	// Verify cert content
	certBytes, err := os.ReadFile(certFile) //nolint:gosec // TempDir-managed path is deterministic and not attacker-controlled
	require.NoError(t, err)

	block, _ := pem.Decode(certBytes)
	require.NotNil(t, block)
	assert.Equal(t, "CERTIFICATE", block.Type)

	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	assert.Equal(t, testNodeIP, cert.Subject.CommonName)
}

func TestGenerateSelfSignedCert_Error(t *testing.T) {
	t.Parallel()
	// Pass invalid path to force error
	err := tls.GenerateSelfSignedCert(testNodeIP, "/nonexistent/dir/cert.pem", "/nonexistent/dir/key.pem")
	require.Error(t, err)
}

func TestRequestCertificateFromK8s(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	certFile := filepath.Join(tmpDir, "cert.pem")
	keyFile := filepath.Join(tmpDir, "key.pem")
	nodeName := "test-node"
	nodeIP := testNodeIP

	client := fake.NewClientset()

	// Add reactor to simulate signing
	client.PrependReactor("get", "certificatesigningrequests", func(action k8stesting.Action) (bool, runtime.Object, error) {
		getAction, ok := action.(k8stesting.GetAction)
		if !ok {
			return false, nil, nil
		}

		if getAction.GetName() == nodeName+"-kubelet-serving" {
			return true, &certificatesv1.CertificateSigningRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: getAction.GetName(),
				},
				Status: certificatesv1.CertificateSigningRequestStatus{
					Certificate: []byte("fake-cert-data"),
					Conditions: []certificatesv1.CertificateSigningRequestCondition{
						{
							Type:   certificatesv1.CertificateApproved,
							Status: corev1.ConditionTrue,
						},
					},
				},
			}, nil
		}

		return false, nil, nil
	})

	err := tls.RequestCertificateFromK8s(context.Background(), client, nodeName, nodeIP, certFile, keyFile)
	require.NoError(t, err)

	assert.FileExists(t, certFile)
	assert.FileExists(t, keyFile)

	content, err := os.ReadFile(certFile) //nolint:gosec // TempDir-managed path is deterministic and not attacker-controlled
	require.NoError(t, err)
	assert.Equal(t, "fake-cert-data", string(content))
}

func TestRequestCertificateFromK8s_Denied(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	certFile := filepath.Join(tmpDir, "cert.pem")
	keyFile := filepath.Join(tmpDir, "key.pem")
	nodeName := "test-node-denied"
	nodeIP := testNodeIP

	client := fake.NewClientset()

	// Add reactor to simulate denial
	client.PrependReactor("get", "certificatesigningrequests", func(action k8stesting.Action) (bool, runtime.Object, error) {
		getAction, ok := action.(k8stesting.GetAction)
		if !ok {
			return false, nil, nil
		}

		if getAction.GetName() == nodeName+"-kubelet-serving" {
			return true, &certificatesv1.CertificateSigningRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: getAction.GetName(),
				},
				Status: certificatesv1.CertificateSigningRequestStatus{
					Conditions: []certificatesv1.CertificateSigningRequestCondition{
						{
							Type:   certificatesv1.CertificateDenied,
							Status: corev1.ConditionTrue,
						},
					},
				},
			}, nil
		}

		return false, nil, nil
	})

	err := tls.RequestCertificateFromK8s(context.Background(), client, nodeName, nodeIP, certFile, keyFile)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CSR was denied")
}

func TestRequestCertificateFromK8s_CreateError(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	certFile := filepath.Join(tmpDir, "cert.pem")
	keyFile := filepath.Join(tmpDir, "key.pem")
	nodeName := "test-node-create-error"
	nodeIP := testNodeIP

	client := fake.NewClientset()
	client.PrependReactor("create", "certificatesigningrequests", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errCreateFailed
	})

	err := tls.RequestCertificateFromK8s(context.Background(), client, nodeName, nodeIP, certFile, keyFile)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create failed")
}

func TestRequestCertificateFromK8s_GetError(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	certFile := filepath.Join(tmpDir, "cert.pem")
	keyFile := filepath.Join(tmpDir, "key.pem")
	nodeName := "test-node-get-error"
	nodeIP := testNodeIP

	client := fake.NewClientset()
	client.PrependReactor("get", "certificatesigningrequests", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errGetFailed
	})

	err := tls.RequestCertificateFromK8s(context.Background(), client, nodeName, nodeIP, certFile, keyFile)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get failed")
}

func TestRequestCertificateFromK8s_ContextCanceled(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	certFile := filepath.Join(tmpDir, "cert.pem")
	keyFile := filepath.Join(tmpDir, "key.pem")
	nodeName := "test-node-timeout"
	nodeIP := testNodeIP

	client := fake.NewClientset()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := tls.RequestCertificateFromK8s(ctx, client, nodeName, nodeIP, certFile, keyFile)
	require.Error(t, err)
}

func TestRequestCertificateFromK8s_WithCA(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	certFile := filepath.Join(tmpDir, "cert.pem")
	keyFile := filepath.Join(tmpDir, "key.pem")
	nodeName := "test-node-ca"
	nodeIP := testNodeIP

	client := fake.NewClientset()

	// Mock CSR signing
	client.PrependReactor("get", "certificatesigningrequests", func(action k8stesting.Action) (bool, runtime.Object, error) {
		getAction, ok := action.(k8stesting.GetAction)
		if !ok {
			return false, nil, nil
		}

		if getAction.GetName() == nodeName+"-kubelet-serving" {
			return true, &certificatesv1.CertificateSigningRequest{
				ObjectMeta: metav1.ObjectMeta{Name: getAction.GetName()},
				Status: certificatesv1.CertificateSigningRequestStatus{
					Certificate: []byte("fake-cert-data"),
					Conditions: []certificatesv1.CertificateSigningRequestCondition{
						{Type: certificatesv1.CertificateApproved, Status: corev1.ConditionTrue},
					},
				},
			}, nil
		}

		return false, nil, nil
	})

	// Mock ConfigMap for CA
	client.PrependReactor("get", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
		getAction, ok := action.(k8stesting.GetAction)
		if !ok {
			return false, nil, nil
		}

		if getAction.GetNamespace() == "kube-system" && getAction.GetName() == "kube-root-ca.crt" {
			return true, &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "kube-root-ca.crt", Namespace: "kube-system"},
				Data:       map[string]string{"ca.crt": "fake-ca-cert"},
			}, nil
		}

		return false, nil, nil
	})

	err := tls.RequestCertificateFromK8s(context.Background(), client, nodeName, nodeIP, certFile, keyFile)
	require.NoError(t, err)

	content, err := os.ReadFile(certFile) //nolint:gosec // TempDir-managed path is deterministic and not attacker-controlled
	require.NoError(t, err)
	// Should contain cert + newline + CA
	assert.Contains(t, string(content), "fake-cert-data")
	assert.Contains(t, string(content), "fake-ca-cert")
}

func TestRequestCertificateFromK8s_WriteCertError(t *testing.T) {
	t.Parallel()

	nodeName := "test-node-write-error"
	nodeIP := testNodeIP

	client := fake.NewClientset()
	// Mock success
	client.PrependReactor("get", "certificatesigningrequests", func(action k8stesting.Action) (bool, runtime.Object, error) {
		getAction, ok := action.(k8stesting.GetAction)
		if !ok {
			return false, nil, nil
		}

		if getAction.GetName() == nodeName+"-kubelet-serving" {
			return true, &certificatesv1.CertificateSigningRequest{
				ObjectMeta: metav1.ObjectMeta{Name: getAction.GetName()},
				Status: certificatesv1.CertificateSigningRequestStatus{
					Certificate: []byte("fake-cert-data"),
					Conditions: []certificatesv1.CertificateSigningRequestCondition{
						{Type: certificatesv1.CertificateApproved, Status: corev1.ConditionTrue},
					},
				},
			}, nil
		}

		return false, nil, nil
	})

	// Invalid path
	err := tls.RequestCertificateFromK8s(context.Background(), client, nodeName, nodeIP, "/nonexistent/dir/cert.pem", "key.pem")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to write certificate")
}

func TestRequestCertificateFromK8s_WriteKeyError(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	certFile := filepath.Join(tmpDir, "cert.pem")
	nodeName := "test-node-write-key-error"
	nodeIP := testNodeIP

	client := fake.NewClientset()
	// Mock success
	client.PrependReactor("get", "certificatesigningrequests", func(action k8stesting.Action) (bool, runtime.Object, error) {
		getAction, ok := action.(k8stesting.GetAction)
		if !ok {
			return false, nil, nil
		}

		if getAction.GetName() == nodeName+"-kubelet-serving" {
			return true, &certificatesv1.CertificateSigningRequest{
				ObjectMeta: metav1.ObjectMeta{Name: getAction.GetName()},
				Status: certificatesv1.CertificateSigningRequestStatus{
					Certificate: []byte("fake-cert-data"),
					Conditions: []certificatesv1.CertificateSigningRequestCondition{
						{Type: certificatesv1.CertificateApproved, Status: corev1.ConditionTrue},
					},
				},
			}, nil
		}

		return false, nil, nil
	})

	// Invalid key path
	err := tls.RequestCertificateFromK8s(context.Background(), client, nodeName, nodeIP, certFile, "/nonexistent/dir/key.pem")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to write private key")
}
