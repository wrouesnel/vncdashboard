package main

import (
	"bitbucket.org/taruti/pbkdf2.go"
	"bitbucket.org/taruti/termios"
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	)


func main() {
	if len(os.Args)!=3 {
		log.Fatal("Usage: "+os.Args[0]+" file user",os.Args)
	}
	bs,e := ioutil.ReadFile(os.Args[1])
	var users [][][]byte
	var found bool
	pass := termios.PasswordConfirm("Password: ", "Confirm: ")
	ph := pbkdf2.HashPassword(pass)
	if e == nil {
		for _,line := range bytes.Split(bs,[]byte("\n")) {
			user := bytes.Split(line, []byte(":"))
			if string(user[0]) == os.Args[2] {
				found = true
				user[1] = []byte(fmt.Sprintf("%X:%X",ph.Salt,ph.Hash))
			}
			users = append(users, user)
		}
	}
	if !found {
		users = append(users, [][]byte{[]byte(os.Args[2]), []byte(fmt.Sprintf("%X:%X",ph.Salt,ph.Hash))})
	}
	var lines [][]byte
	for _,line := range users {
		lines = append(lines, bytes.Join(line, []byte(":")))
	}
	raw := bytes.Join(lines, []byte("\n"))
	ioutil.WriteFile(os.Args[1], raw, 0600)
}
