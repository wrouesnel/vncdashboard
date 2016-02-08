package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/asn1"
	"encoding/pem"
	"io/ioutil"
	"github.com/prometheus/common/log"
	"math/big"
	"time"
	"os"
)

func EnsureCert(hostname string, sslCert string, sslKey string) {

	if _, err := os.Stat(sslKey); os.IsNotExist(err) {
		log.Warn("Generating non-existent SSL key file:", sslKey)

		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}

		kr := x509.MarshalPKCS1PrivateKey(key)
		kb := pem.Block{"RSA PRIVATE KEY", nil, kr}
		ioutil.WriteFile(sslKey, pem.EncodeToMemory(&kb), 0400)
	}

	// If non-existent public key, derive from private key
	if _, err := os.Stat(sslCert); os.IsNotExist(err) {
		log.Warn("Deriving non-existent SSL public certificate file:", sslCert)

		privateKey, err := ioutil.ReadFile(sslKey)
		if err != nil {
			log.Fatalln("Couldn't read private key file:", err)
		}

		key, err := x509.ParsePKCS1PrivateKey(privateKey)
		if err != nil {
			log.Fatalln("Couldn't parse private key file:", err)
		}

		tmpl := new(x509.Certificate)
		tmpl.SerialNumber = big.NewInt(1)
		tmpl.NotBefore = time.Now()
		tmpl.NotAfter  = tmpl.NotBefore.AddDate(10,0,0)
		tmpl.Subject.CommonName = hostname
		tmpl.Subject.Organization = []string{exeName}
		tmpl.SubjectKeyId = []byte{1, 2, 3, 4}
		tmpl.BasicConstraintsValid = true
		tmpl.IsCA = true
		tmpl.DNSNames = []string{hostname}
		tmpl.PolicyIdentifiers = []asn1.ObjectIdentifier{[]int{1, 2, 3}}

		cacert := tmpl
		cakey  := key

		cr, err := x509.CreateCertificate(rand.Reader, tmpl, cacert, &key.PublicKey, cakey)
		if err != nil {
			panic(err)
		}

		cb := pem.Block{"CERTIFICATE", nil, cr}
		ioutil.WriteFile(sslCert, pem.EncodeToMemory(&cb), 0422)
	}

	return
}
