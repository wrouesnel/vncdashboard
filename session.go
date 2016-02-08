package main

import (
	"code.google.com/p/gorilla/securecookie"
	"code.google.com/p/gorilla/sessions"
	"net/http"
	"time"
	)


var store = sessions.NewCookieStore(
	securecookie.GenerateRandomKey(32),
	securecookie.GenerateRandomKey(32))

func GetUser(w http.ResponseWriter, r *http.Request) string {
	session,_ := store.Get(r,"login")
	u, _ := session.Values["user"].(string)
	t, _ := session.Values["time"].(int64)
	now  := time.Now().Unix()
	if now-t > 3600 { u = "" }
	if u=="" {
		http.Redirect(w,r,"/",302)
	}
	return u
}

func SetUser(w http.ResponseWriter, r *http.Request, user string) {
	session,_ := store.Get(r,"login")
	session.Values["user"] = user
	session.Values["time"] = time.Now().Unix()
	session.Save(r,w)
}
