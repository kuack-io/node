package tls

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

const (
	csrRequestTimeout  = 2 * time.Minute
	csrPollInterval    = 2 * time.Second
	certFilePermission = 0o600
	keyFilePermission  = 0o600
)

var (
	errCSRSigningTimeout = errors.New("timeout waiting for CSR to be signed")
	errCSRDenied         = errors.New("CSR was denied")
)

// RequestCertificateFromK8s requests a certificate from Kubernetes Certificate API.
// This is the proper way to get certificates for kubelet endpoints.
func RequestCertificateFromK8s(ctx context.Context, client kubernetes.Interface, nodeName, nodeIP string, certFile, keyFile string) error {
	klog.Infof("Requesting TLS certificate from Kubernetes CSR API for node %s (IP: %s)", nodeName, nodeIP)

	// Generate private key
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("failed to generate private key: %w", err)
	}

	// Create CSR
	csrPEM, err := generateCSR(priv, nodeName, nodeIP)
	if err != nil {
		return fmt.Errorf("failed to generate CSR: %w", err)
	}

	// Create CertificateSigningRequest object
	csrName := nodeName + "-kubelet-serving"
	csrObj := &certificatesv1.CertificateSigningRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name: csrName,
		},
		Spec: certificatesv1.CertificateSigningRequestSpec{
			Request:    csrPEM,
			SignerName: certificatesv1.KubeletServingSignerName,
			Usages: []certificatesv1.KeyUsage{
				certificatesv1.UsageDigitalSignature,
				certificatesv1.UsageKeyEncipherment,
				certificatesv1.UsageServerAuth,
			},
		},
	}

	// Delete existing CSR if it exists
	_ = client.CertificatesV1().CertificateSigningRequests().Delete(ctx, csrName, metav1.DeleteOptions{})

	// Submit CSR
	_, err = client.CertificatesV1().CertificateSigningRequests().Create(ctx, csrObj, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create CSR: %w", err)
	}

	klog.Infof("CSR %s created, waiting for approval...", csrName)

	// Wait for CSR to be approved and signed (with timeout)
	ctxWithTimeout, cancel := context.WithTimeout(ctx, csrRequestTimeout)
	defer cancel()

	var certPEM []byte

	ticker := time.NewTicker(csrPollInterval)
	defer ticker.Stop()

pollLoop:
	for {
		select {
		case <-ctxWithTimeout.Done():
			return errCSRSigningTimeout
		case <-ticker.C:
			csrObj, err = client.CertificatesV1().CertificateSigningRequests().Get(ctx, csrName, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("failed to get CSR: %w", err)
			}

			// Check if approved
			approved := false

			for _, condition := range csrObj.Status.Conditions {
				if condition.Type == certificatesv1.CertificateApproved {
					approved = true

					break
				}

				if condition.Type == certificatesv1.CertificateDenied {
					return fmt.Errorf("%w: %s", errCSRDenied, condition.Message)
				}
			}

			if !approved {
				// Try to auto-approve (this requires appropriate RBAC permissions)
				csrObj.Status.Conditions = append(csrObj.Status.Conditions, certificatesv1.CertificateSigningRequestCondition{
					Type:           certificatesv1.CertificateApproved,
					Status:         corev1.ConditionTrue,
					Reason:         "AutoApproved",
					Message:        "Auto-approved by kuack-node",
					LastUpdateTime: metav1.Now(),
				})

				_, err = client.CertificatesV1().CertificateSigningRequests().UpdateApproval(ctx, csrName, csrObj, metav1.UpdateOptions{})
				if err != nil {
					klog.Warningf("Failed to auto-approve CSR (this is expected if auto-approval is not enabled): %v", err)
				}

				continue
			}

			// Check if signed
			if len(csrObj.Status.Certificate) > 0 {
				certPEM = csrObj.Status.Certificate

				break pollLoop
			}
		}
	}

	klog.Infof("Certificate received from Kubernetes CSR API")

	// Extract the CA certificate from the chain
	// K8s CSR API returns just the certificate, not the full chain
	// We need to append the CA cert to make a full chain for TLS
	caCert, err := getClusterCA(ctx, client)
	if err != nil {
		klog.Warningf("Failed to get cluster CA certificate: %v. Certificate chain may be incomplete.", err)
	}

	// Write certificate chain to file (cert + CA if available)
	certChain := certPEM
	if len(caCert) > 0 {
		// Append CA certificate to make full chain
		certChain = append(certChain, '\n')
		certChain = append(certChain, caCert...)
	}

	err = os.WriteFile(certFile, certChain, certFilePermission)
	if err != nil {
		return fmt.Errorf("failed to write certificate: %w", err)
	}

	// Write private key to file
	privBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return fmt.Errorf("failed to marshal private key: %w", err)
	}

	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: privBytes,
	})

	err = os.WriteFile(keyFile, keyPEM, keyFilePermission)
	if err != nil {
		return fmt.Errorf("failed to write private key: %w", err)
	}

	klog.Infof("Certificate and key written to %s and %s", certFile, keyFile)

	return nil
}

func generateCSR(priv *ecdsa.PrivateKey, nodeName, nodeIP string) ([]byte, error) {
	ip := net.ParseIP(nodeIP)

	var ipAddresses []net.IP
	if ip != nil {
		ipAddresses = []net.IP{ip}
	}

	template := &x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName:   "system:node:" + nodeName,
			Organization: []string{"system:nodes"},
		},
		DNSNames:    []string{nodeName, nodeName + ".local"},
		IPAddresses: ipAddresses,
	}

	csrDER, err := x509.CreateCertificateRequest(rand.Reader, template, priv)
	if err != nil {
		return nil, fmt.Errorf("failed to create certificate request: %w", err)
	}

	return pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE REQUEST",
		Bytes: csrDER,
	}), nil
}

// getClusterCA retrieves the cluster's CA certificate from the ConfigMap.
func getClusterCA(ctx context.Context, client kubernetes.Interface) ([]byte, error) {
	// Try to get the CA from the kube-root-ca.crt ConfigMap (standard in Kubernetes 1.20+)
	cm, err := client.CoreV1().ConfigMaps("kube-system").Get(ctx, "kube-root-ca.crt", metav1.GetOptions{})
	if err == nil {
		if caCert, ok := cm.Data["ca.crt"]; ok {
			return []byte(caCert), nil
		}
	}

	// Fallback: try to read from the service account token location
	caCertPath := "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"

	caCert, err := os.ReadFile(caCertPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read CA certificate: %w", err)
	}

	return caCert, nil
}
