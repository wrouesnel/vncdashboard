package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/asn1"
	"encoding/pem"
	"io/ioutil"
	"log"
	"math/big"
	"time"
)

func EnsureCert(DNS string, ConfigDir string) {
	cf := ConfigDir + "/novncgo." + DNS + ".cert"
	kf := ConfigDir + "/novncgo." + DNS + ".secret"
	_, err := tls.LoadX509KeyPair(cf, kf)
	if err != nil {
		log.Println("Generating certificate ",cf)
		tmpl := new(x509.Certificate)
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		cacert := tmpl
		cakey  := key
		
		tmpl.SerialNumber = big.NewInt(1)
		tmpl.NotBefore = time.Now()
		tmpl.NotAfter  = tmpl.NotBefore.AddDate(10,0,0)
		tmpl.Subject.CommonName = DNS
		tmpl.Subject.Organization = []string{"novncgo"}
		tmpl.SubjectKeyId = []byte{1, 2, 3, 4}
		tmpl.BasicConstraintsValid = true
		tmpl.IsCA = true
		tmpl.DNSNames = []string{DNS}
		tmpl.PolicyIdentifiers = []asn1.ObjectIdentifier{[]int{1, 2, 3}}
		cr, err := x509.CreateCertificate(rand.Reader, tmpl, cacert, &key.PublicKey, cakey)
		if err != nil {
			panic(err)
		}
		kr := x509.MarshalPKCS1PrivateKey(key)
		kb := pem.Block{"RSA PRIVATE KEY", nil, kr}
		cb := pem.Block{"CERTIFICATE", nil, cr}
		ioutil.WriteFile(kf, pem.EncodeToMemory(&kb), 0400)
		cm := pem.EncodeToMemory(&cb)
		ioutil.WriteFile(cf, cm, 0400)

	}
	return
}
