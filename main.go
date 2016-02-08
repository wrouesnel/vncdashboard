package main

import (
	"code.google.com/p/go.net/websocket"
	"encoding/base64"
	"flag"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	)

var (
	hostname,_ = os.Hostname()
    listen  = flag.String("listen", ":6080", "Location to listen for connections")
    host    = flag.String("hostname", hostname, "Hostname to use for certificate")
    fileDir = flag.String("filedir", "export", "Directory for exported files")
)

//func DebugHandler(w http.ResponseWriter, req *http.Request) {
//	log.Println(req)
//	http.DefaultServeMux.ServeHTTP(w,req)
//}

func main() {
	flag.Parse()
	EnsureCert(*host, ".")
	http.HandleFunc("/file/", ufh)
	http.HandleFunc("/list", lh)
 	http.HandleFunc("/login", loginh)
 	http.HandleFunc("/logout", logouth)
	http.HandleFunc("/upload", uploadh)
	http.HandleFunc("/websockify/", wsh)
	http.HandleFunc("/", fh)
	log.Fatal(http.ListenAndServeTLS(*listen, "novnc."+*host+".cert", "novnc."+*host+".secret", nil))
}


func loginh(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" { return }
	u := r.FormValue("user")
	p := r.FormValue("pass")
	if !AuthUser(u,p) {
		http.Redirect(w,r,"/",302)
		return
	}
	log.Print("Logging in ",u)
	SetUser(w,r,u)
	http.Redirect(w,r,"/list",302)
}

func logouth(w http.ResponseWriter, r *http.Request) {
	SetUser(w,r,"")
	http.Redirect(w,r,"/",302)
}

type List struct {
	Servers []Server
	User string
}

func lh(w http.ResponseWriter, r *http.Request) {
 	user := GetUser(w,r)
	if user=="" { return }
	tmpl, _ := template.ParseFiles("tmpl/list.html")
	tmpl.Execute(w,List{Servers(),user})
}
func fh(w http.ResponseWriter, r *http.Request) {
//	log.Println(r.RequestURI)
	http.FileServer(http.Dir("static")).ServeHTTP(w,r)
}

func ufh(w http.ResponseWriter, r *http.Request) {
 	user := GetUser(w,r)
	if user=="" { return }
	http.StripPrefix("/file",http.FileServer(http.Dir(*fileDir))).ServeHTTP(w,r)
}

func uploadh(w http.ResponseWriter, r *http.Request) {
 	user := GetUser(w,r)
	if user=="" { return }
	f,h,e := r.FormFile("file")
	if e!=nil { return }
	defer f.Close()
	dst,e := os.Create(*fileDir+"/"+SafeName(h.Filename))
	if e!=nil { return }
	defer dst.Close()
	io.Copy(dst,f)
	http.Redirect(w,r,"/list",302)
}

func SafeName(s string) string {
	return strings.Replace(s, "/", "_", -1)
}

func wsh(w http.ResponseWriter, r *http.Request) {
	log.Println(r.RequestURI)
 	user := GetUser(w,r)
	if user=="" {
		log.Print("Invalid user in wsh")
		return 
	}

	li := strings.LastIndex(r.RequestURI, "/")
	sname := r.RequestURI[li+1:]
	var loc string
	for _,s := range Servers() {
		log.Print("Looping ",s.Name," searching for ",sname)
		if s.Name == sname {
			loc = s.Location
			goto connect
		}
	}
	log.Print("Invalid server name given")
	return
connect:
	log.Print("Opening vnc connection for ",user," to ",loc)
	vc,err := net.Dial("tcp",loc)
	defer vc.Close()
	if err!=nil {
		log.Print(err)
		return
	}
	websocket.Handler(func(ws *websocket.Conn) {
		go func() {
			sbuf := make([]byte, 32*1024)
			dbuf := make([]byte, 32*1024)
			for {
				n,e := ws.Read(sbuf)
//				log.Println("<< R",n,e)
				if e!=nil { return }
				n,e  = base64.StdEncoding.Decode(dbuf, sbuf[0:n])
				if e!=nil { return }
				n,e  = vc.Write(dbuf[0:n])
				if e!=nil { return }
			}
		}()
		func() {
			sbuf := make([]byte, 32*1024)
			dbuf := make([]byte, 64*1024)
			for {
				n,e := vc.Read(sbuf)
//				log.Println(">> R ",n)
				if e!=nil { return }
				base64.StdEncoding.Encode(dbuf,sbuf[0:n])
				n = ((n+2)/3)*4
				ws.Write(dbuf[0:n])
				if e!=nil { return }
			}
			
		}()
	}).ServeHTTP(w,r)
}

