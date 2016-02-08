package main

import (
	"github.com/gorilla/websocket"
	"flag"
	"html/template"
	"io"
	"github.com/prometheus/common/log"
	"net"
	"net/http"
	"os"
	"strings"
	"io/ioutil"
	"github.com/kardianos/osext"
	"path"
	"gopkg.in/fsnotify.v1"
	"path/filepath"
	"sync"
)

var defaultConfigDir = "."

var (
	hostname,_ = os.Hostname()
	exePath, _ = osext.Executable()
	exeName = path.Base(exePath)

    fileDir = flag.String("filedir", "export", "Directory for exported files")

	listen  = flag.String("listen.addr", ":6080", "Location to listen for connections")
	host    = flag.String("listen.hostname", hostname, "Hostname to use for certificate")
	sslCert = flag.String("listen.ssl.cert", exeName + "." + hostname + ".crt", "Path to SSL certfile. Will be generated if it does not exist." )
	sslKey 	= flag.String("listen.ssl.key", exeName + "." + hostname + ".key", "Path to SSL keyfile. Will be generated if it does not exist." )

	socketPaths = flag.String("servers.watch-glob", "", "Glob path to watch for VNC UNIX socket servers appearing")
)

var wsupgrader = websocket.Upgrader{}

// Watch paths for VNC
var socketWatcher = func() *fsnotify.Watcher {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		panic(err)
	}
	return w
}()

// Defines a VNC server
type VNCServer struct {
	NetType string	// Network type in Go form
	Address string	// Address in Go form
	Encrypted bool	// Is password encrypted?
	Salt string		// Encrypted password salt
	Username string	// VNC username
	Password string	// VNC password
}

// Maintains the list of currently available VNC files
type serverManager struct {
	availableServers map[string]VNCServer
	mtx sync.RWMutex
}

// Add a server to the list
func (this *serverManager) Add(server VNCServer) {
	this.mtx.Lock()
	defer this.mtx.Unlock()

	this.availableServers[server.NetType + ":" + server.Address] = server
}

// Remove a server from the list by it's network type and address
func (this *serverManager) RemoveByAddress(nettype string, addr string) {
	this.mtx.Lock()
	defer this.mtx.Unlock()

	delete(this.availableServers, nettype + ":" + addr)
}

// Return list of currently known server objects
func (this *serverManager) List() []VNCServer {
	this.mtx.RLock()
	defer this.mtx.RUnlock()

	r := []VNCServer{}
	for _, v := range this.availableServers {
		r = append(r, v)
	}

	return r
}

func NewServerManager() *serverManager {
	m := serverManager{}
	m.availableServers = make(map[string]VNCServer)
	return &m
}

func watchSocketFiles(events <-chan fsnotify.Event, glob string, manager *serverManager) {
	for e := range events {
		// Glob the path against the watch glob
		matches, _ := filepath.Glob(e.Name)
		if len(matches) > 0 {
			switch (e.Op) {
			case fsnotify.Create:
				server := VNCServer{
					NetType: "unix",
					Address: e.Name,
				}
				manager.Add(server)
			case fsnotify.Remove, fsnotify.Rename:
				// Remove and rename have same relative effect - server no longer available
				manager.RemoveByAddress("unix", e.Name)
			default:
				// Ignore
			}
		}
	}
}

func main() {
	flag.Parse()

	// ensure the certificates of some sort exist
	EnsureCert(*host, *sslCert, *sslKey)

	// Setup a new server manager
	manager := NewServerManager()

	// Glob the watch paths, reduce to minimal set and establish watches.
	if *socketPaths != "" {
		watchPaths, err := filepath.Glob(*socketPaths)
		if err != nil {
			log.Fatalln("Error globbing watch-dirs:", err)
		}
		reducedPaths := make(map[string]interface{})
		for _, globPath := range watchPaths {
			log.Debugln("Glob matches", globPath)
			dirPath := path.Dir(globPath)
			reducedPaths[dirPath] = nil
		}

		for dirPath, _ := range reducedPaths {
			log.Infoln("Adding watch path", dirPath)
			socketWatcher.Add(dirPath)
		}
	}

	// Setup a listener service to add/remove VNC targets
	go watchSocketFiles(socketWatcher.Events, *socketPaths, manager)

	http.HandleFunc("/file/", ufh)
	http.HandleFunc("/list", lh)
 	http.HandleFunc("/login", loginh)
 	http.HandleFunc("/logout", logouth)
	http.HandleFunc("/upload", uploadh)
	http.HandleFunc("/websockify/", wsh)
	http.HandleFunc("/", fh)

	log.Fatal(http.ListenAndServeTLS(*listen, *sslCert, *sslKey, nil))
}

func loginh(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" { return }
	u := r.FormValue("user")
	p := r.FormValue("pass")
	if !AuthUser(u,p) {
		http.Redirect(w,r,"/",302)
		return
	}
	log.Infoln("Logging in ",u)
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
//	log.Infoln(r.RequestURI)
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
	log.Infoln(r.RequestURI)
 	user := GetUser(w,r)
	if user=="" {
		log.Infoln("Invalid user in wsh")
		return 
	}

	li := strings.LastIndex(r.RequestURI, "/")
	sname := r.RequestURI[li+1:]
	var loc string
	for _,s := range Servers() {
		log.Infoln("Looping ",s.Name," searching for ",sname)
		if s.Name == sname {
			loc = s.Location
			goto connect
		}
	}
	log.Infoln("Invalid server name given")
	return
connect:
	log.Infoln("Opening vnc connection for ",user," to ",loc)
	vc,err := net.Dial("tcp",loc)
	defer vc.Close()
	if err!=nil {
		log.Infoln(err)
		return
	}

	conn, err := wsupgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Infoln("Websocket Upgrade:", err)
		return
	}
	defer conn.Close()

	// Write worker
	go func() {
		// Read loop
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				log.Infoln("Websocket READ:", err)
				break
			}
			_, err = vc.Write(message)
			if err != nil {
				break
			}
		}
	}()
	// Read worker
	for {
		message, err := ioutil.ReadAll(vc)
		if err != nil {
			log.Infoln("VNC READ:", err)
			break
		}
		err = conn.WriteMessage(websocket.BinaryMessage, message)
		if err != nil {
			log.Infoln("Websocket WRITE:", err)
			break
		}
	}
}

