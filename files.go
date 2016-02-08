package main

import (
	"bitbucket.org/taruti/pbkdf2.go"
	"github.com/bmatsuo/csvutil"
	"encoding/hex"
	"log"
	"os"
	"strings"
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
		log.Print(e)
		return false
	}
	lines,e := csvutil.NewReader(r,csvcfg).RemainingRows()
	if e!=nil {
		log.Print(e)
		return false
	}
	for _,line := range lines {
		if len(line)>=3 && line[0] == username {
			var ph pbkdf2.PasswordHash
			ph.Salt,_ = hex.DecodeString(line[1])
			ph.Hash,_ = hex.DecodeString(line[2])
			return pbkdf2.MatchPassword(pass, ph)
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
		log.Print(e)
		return nil
	}
	lines,e := csvutil.NewReader(r,csvcfg).RemainingRows()
	if e!=nil {
		log.Print(e)
		return nil
	}
	ss := []Server{}
	for _,line := range lines {
		ss = append(ss,Server{line[0], line[1]+":"+line[2], strings.Split(line[3]," ")})
	}
	return ss
}
