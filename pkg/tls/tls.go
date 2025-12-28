package tls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"k8s.io/klog/v2"
)

const serialNumberBits = 128

// GenerateSelfSignedCert generates a self-signed TLS certificate for the given IP address.
// It creates cert.pem and key.pem files in the specified directory.
func GenerateSelfSignedCert(ip string, certFile, keyFile string) error {
	klog.Infof("Generating self-signed TLS certificate for IP: %s", ip)

	// Generate private key
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("failed to generate private key: %w", err)
	}

	// Create certificate template
	serialLimit := new(big.Int).Lsh(big.NewInt(1), serialNumberBits)

	serialNumber, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return fmt.Errorf("failed to generate serial number: %w", err)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"Kuack"},
			CommonName:   ip,
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour), // Valid for 1 year
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP(ip)},
		DNSNames:              []string{"localhost"},
	}

	// Create certificate
	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return fmt.Errorf("failed to create certificate: %w", err)
	}

	cleanCertFile := filepath.Clean(certFile)
	cleanKeyFile := filepath.Clean(keyFile)

	// Write certificate to file
	certOut, err := os.Create(cleanCertFile)
	if err != nil {
		return fmt.Errorf("failed to create cert file: %w", err)
	}

	defer func() {
		closeErr := certOut.Close()
		if closeErr != nil {
			klog.Errorf("Failed to close cert file %s: %v", cleanCertFile, closeErr)
		}
	}()

	encodeCertErr := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	if encodeCertErr != nil {
		return fmt.Errorf("failed to write cert: %w", encodeCertErr)
	}

	klog.Infof("Certificate written to %s", cleanCertFile)

	// Write private key to file
	keyOut, err := os.Create(cleanKeyFile)
	if err != nil {
		return fmt.Errorf("failed to create key file: %w", err)
	}

	defer func() {
		closeErr := keyOut.Close()
		if closeErr != nil {
			klog.Errorf("Failed to close key file %s: %v", cleanKeyFile, closeErr)
		}
	}()

	privBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return fmt.Errorf("failed to marshal private key: %w", err)
	}

	encodeKeyErr := pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: privBytes})
	if encodeKeyErr != nil {
		return fmt.Errorf("failed to write key: %w", encodeKeyErr)
	}

	klog.Infof("Private key written to %s", cleanKeyFile)

	return nil
}
