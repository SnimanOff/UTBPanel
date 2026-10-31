//go:build windows

// Command tpm checks that an ECDSA P-256 key kept in the TPM can drive a
// TLS 1.3 client certificate handshake from Go (plan section 2.2).
package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func main() {
	noTPM := flag.Bool("no-tpm", false, "use a software key protected by DPAPI")
	name := flag.String("name", "tgpanel-tpm-experiment", "key name")
	del := flag.Bool("delete", false, "delete the key and exit")
	flag.Parse()

	var signer crypto.Signer
	if *noTPM {
		path := filepath.Join(os.TempDir(), *name+".dpapi")
		if *del {
			fmt.Println(os.Remove(path))
			return
		}
		k, created, err := openOrCreateDPAPIKey(path)
		if err != nil {
			log.Fatalf("dpapi key: %v", err)
		}
		fmt.Printf("dpapi key %s (created: %v)\n", path, created)
		signer = k
	} else {
		k, created, err := openOrCreateTPMKey(*name)
		if err != nil {
			log.Fatalf("tpm key: %v", err)
		}
		defer k.Close()
		if *del {
			fmt.Println(k.Delete())
			return
		}
		fmt.Printf("tpm key %q (created: %v)\n", *name, created)
		if err := k.tryExportPrivate(); err == nil {
			log.Fatal("FAIL: private key was exported")
		} else {
			fmt.Printf("private key export refused: %v\n", err)
		}
		signer = k
	}

	measureSign(signer)
	if err := handshake(signer); err != nil {
		log.Fatalf("FAIL: %v", err)
	}
	fmt.Println("OK")
}

func measureSign(s crypto.Signer) {
	digest := sha256.Sum256([]byte("tgpanel"))
	const n = 10
	start := time.Now()
	for range n {
		sig, err := s.Sign(rand.Reader, digest[:], crypto.SHA256)
		if err != nil {
			log.Fatalf("sign: %v", err)
		}
		if !ecdsa.VerifyASN1(s.Public().(*ecdsa.PublicKey), digest[:], sig) {
			log.Fatal("FAIL: signature does not verify")
		}
	}
	fmt.Printf("sign: %v per signature\n", time.Since(start)/n)
}

func handshake(signer crypto.Signer) error {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	caTpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	if err != nil {
		return err
	}
	ca, _ := x509.ParseCertificate(caDER)
	pool := x509.NewCertPool()
	pool.AddCert(ca)

	issue := func(serial int64, pub crypto.PublicKey, usage x509.ExtKeyUsage, ip net.IP) ([]byte, error) {
		tpl := &x509.Certificate{
			SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "leaf"},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage},
		}
		if ip != nil {
			tpl.IPAddresses = []net.IP{ip}
		}
		return x509.CreateCertificate(rand.Reader, tpl, ca, pub, caKey)
	}
	srvKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	srvDER, err := issue(2, &srvKey.PublicKey, x509.ExtKeyUsageServerAuth, net.IPv4(127, 0, 0, 1))
	if err != nil {
		return err
	}
	cliDER, err := issue(3, signer.Public(), x509.ExtKeyUsageClientAuth, nil)
	if err != nil {
		return err
	}
	srvCert := tls.Certificate{Certificate: [][]byte{srvDER}, PrivateKey: srvKey}
	cliCert := tls.Certificate{Certificate: [][]byte{cliDER}, PrivateKey: signer}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{srvCert},
		ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool,
	})
	if err != nil {
		return err
	}
	defer ln.Close()
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello %s", r.TLS.PeerCertificates[0].Subject.CommonName)
	}))

	offered := false
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		MinVersion: tls.VersionTLS13, RootCAs: pool,
		GetClientCertificate: func(cri *tls.CertificateRequestInfo) (*tls.Certificate, error) {
			for _, s := range cri.SignatureSchemes {
				offered = offered || s == tls.ECDSAWithP256AndSHA256
			}
			return &cliCert, cri.SupportsCertificate(&cliCert)
		},
	}}}
	start := time.Now()
	resp, err := client.Get("https://" + ln.Addr().String())
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	fmt.Printf("handshake + request: %v, response %q\n", time.Since(start), body)
	if !offered {
		return errors.New("server did not offer ecdsa_secp256r1_sha256")
	}
	fmt.Println("signature scheme: ecdsa_secp256r1_sha256")
	return nil
}
