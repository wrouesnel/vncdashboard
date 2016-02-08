package main

import (
	"golang.org/x/crypto/pbkdf2"
	"github.com/bmatsuo/csvutil"
	"encoding/hex"
	"github.com/prometheus/common/log"
	"os"
	"strings"

	"crypto/sha1"
	"bytes"
)

var csvcfg = csvcfgI()

func csvcfgI() *csvutil.Config {
	c := *csvutil.DefaultConfig
	c.Sep = ':'
	return &c
}

func AuthUser(username, pass string) bool {
	r,e := os.Open("novncgo.passwd")
	if e!=nil {
		log.Infoln(e)
		return false
	}
	lines,e := csvutil.NewReader(r,csvcfg).RemainingRows()
	if e!=nil {
		log.Infoln(e)
		return false
	}
	for _,line := range lines {
		if len(line)>=3 && line[0] == username {
			// Get stored salt and hash
			salt, _ := hex.DecodeString(line[1])
			hash, _ := hex.DecodeString(line[2])

			// Check password against it
			calc_hash := pbkdf2.Key([]byte(pass), salt, 4096, 32, sha1.New)

			return (bytes.Compare(hash, calc_hash) == 0)
		}
	}
	return false
}

type Server struct {
	Name, Location string
	Perms []string
}

func Servers() []Server {
	r,e := os.Open("novncgo.servers")
	if e!=nil {
		log.Infoln(e)
		return nil
	}
	lines,e := csvutil.NewReader(r,csvcfg).RemainingRows()
	if e!=nil {
		log.Infoln(e)
		return nil
	}
	ss := []Server{}
	for _,line := range lines {
		ss = append(ss,Server{line[0], line[1]+":"+line[2], strings.Split(line[3]," ")})
	}
	return ss
}
