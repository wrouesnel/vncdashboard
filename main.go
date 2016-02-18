package main

import (
	"github.com/gorilla/websocket"
	"flag"
	"github.com/prometheus/common/log"
	"net"
	"net/http"
	"os"
	"strings"
	"github.com/kardianos/osext"
	"path"
	"gopkg.in/fsnotify.v1"
	"path/filepath"
	"sync"
	"github.com/julienschmidt/httprouter"
	"mime"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"encoding/json"
	"crypto/md5"
	"github.com/JanBerktold/sse"
	"net/http/httputil"
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

	insecure = flag.Bool("listen.ssl.disable", false, "Disable SSL entirely (INSECURE!)")

	socketPaths = flag.String("servers.watch-glob", "", "Glob path to watch for VNC UNIX socket servers appearing")

	debugWeb = flag.String("debug.webapp-proxy", "", "Proxy all requests for static assets to this IP instead")
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

// VNC server
type VNCServer interface {}

type vncServer struct {
	NetType string	`json:"nettype"`// Golang network type
	Address string	`json:"address"`// Address
	Username string	`json:"username"`// Username
	Password string	`json:"-"`// Password
}

// Types used for publishing server events
type addedServer vncServer
type removedServer vncServer

func ParseVNCServer(address string) vncServer {
	urlp, err := url.Parse(address)
	if err != nil {
		return vncServer{}
	}

	user := ""
	password := ""
	if urlp.User != nil {
		user = urlp.User.Username()
		password, _ = urlp.User.Password()
	}

	if urlp.Path != "" {	// file-likes
		return vncServer{
			NetType: urlp.Scheme,
			Address: urlp.Path,
			Username: user,
			Password: password,
		}
	} else {	// actual network sockets
		return vncServer{
			NetType: urlp.Scheme,
			Address: urlp.Host,
			Username: user,
			Password: password,
		}
	}
}

// String representation (url form)
func (this vncServer) String() string {
	u := url.URL{}
	u.Host = this.Address
	u.Scheme = this.NetType
	u.User = url.UserPassword(this.Username, this.Password)

	return u.String()
}

// Short representation (used internally) for the manager and access paths.
// This is just the MD5 hash of the URL form representation, in hex
func (this vncServer) Short() string {
	hasher := md5.New()
	hasher.Write([]byte(this.String()))

	return hex.EncodeToString(hasher.Sum(nil))
}

// Maintains the list of currently available VNC files
type serverManager struct {
	availableServers map[string]vncServer
	subscribers []chan VNCServer	// Subscribers requesting server updates
	mtx sync.RWMutex
	smtx sync.Mutex
}

// Request a channel that publishes updates
func (this *serverManager) Subscribe() chan VNCServer {
	this.smtx.Lock()
	defer this.smtx.Unlock()

	ch := make(chan VNCServer,1)
	this.subscribers = append(this.subscribers, ch)
	return ch
}

func (this *serverManager) Unsubscribe(ch chan VNCServer) {
	this.smtx.Lock()
	defer this.smtx.Unlock()

	for idx, kch := range this.subscribers {
		if ch == kch {
			this.subscribers = append(this.subscribers[:idx], this.subscribers[idx+1:]...)
			break
		}
	}
}

func (this *serverManager) publish(action VNCServer) {
	for _, ch := range this.subscribers {
		// Always send messages
		select {
		case ch <- action: continue
		default:
			log.Infoln("Dropping message due to full channel")
		}
	}
}

// Add a server to the list
func (this *serverManager) Add(server vncServer) {
	this.mtx.Lock()
	defer this.mtx.Unlock()

	log.Debugln("Adding server:", server)
	this.availableServers[server.Short()] = server
	this.publish(addedServer(server))
}

// Remove a server from the list by it's network type and address
func (this *serverManager) RemoveByAddress(address string) {
	this.mtx.Lock()
	defer this.mtx.Unlock()

	log.Debugln("Removing server:", address)

	toRemove := []string{}
	for k, v := range this.availableServers {
		if v.Address == address {
			toRemove = append(toRemove, k)
		}
	}

	for _, k := range toRemove {
		this.publish(removedServer(this.availableServers[k]))
		delete(this.availableServers, k)
	}
}

// Make a deep-copy list of the current map
func (this *serverManager) List() map[string]vncServer {
	this.mtx.RLock()
	defer this.mtx.RUnlock()

	r := make(map[string]vncServer)
	for k, v := range this.availableServers {
		r[k] = v
	}

	return r
}

func NewServerManager() *serverManager {
	m := serverManager{}
	m.availableServers = make(map[string]vncServer)
	return &m
}

func watchSocketFiles(events <-chan fsnotify.Event, glob string, manager *serverManager) {
	// Do an initial sweep of the glob path to pickup existing files. Run async so we don't
	// miss events.

	// Glob the watch paths, reduce to minimal set and establish watches.
	if *socketPaths != "" {
		log.Debugln("Watch glob supplied:", *socketPaths)
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

	// Pickup initial matches asynchronously
	go func() {
		watchPaths, err := filepath.Glob(*socketPaths)
		if err != nil {
			log.Fatalln("Error globbing watch-dirs:", err)
		}

		for _, globPath := range watchPaths {
			// Add existent files to the manager
			if s, err := os.Stat(globPath); !s.Mode().IsDir() && !os.IsNotExist(err) {
				log.Infoln("Adding existing matched path:", globPath)
				server := vncServer{
					NetType: "unix",
					Address: globPath,
				}
				manager.Add(server)
			}
		}
	}()

	for e := range events {
		log.Debugln("WATCHED:", e.String())
		// Check the path against the glob (which should generally filter for something like
		// socket.
		matched, _ := filepath.Match(glob, e.Name)
		if matched {
			switch (e.Op) {
			case fsnotify.Create:
				server := vncServer{
					NetType: "unix",
					Address: e.Name,
				}
				manager.Add(server)
			case fsnotify.Remove, fsnotify.Rename:
				// Remove and rename have same relative effect - server no longer available
				manager.RemoveByAddress(e.Name)
			default:
				// Ignore
			}
		}
	}
}

func main() {
	flag.Parse()
	log.Debugln("Log level set to debug")

	// ensure the certificates of some sort exist
	if !*insecure {
		EnsureCert(*host, *sslCert, *sslKey)
	}

	// Setup a new server manager
	manager := NewServerManager()

	if *socketPaths != "" {
		// Setup a listener service to add/remove VNC targets
		go watchSocketFiles(socketWatcher.Events, *socketPaths, manager)
	}

	// Router
	router := httprouter.New()

	var debugReverseProxy *httputil.ReverseProxy
	if *debugWeb != "" {
		log.Infoln("Proxy debugging enabled to", *debugWeb)
		debugReverseProxy = httputil.NewSingleHostReverseProxy(*debugWeb)
	}

	// Static files endpoint
	router.GET("/static/*filepath", func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		// In debugging mode, proxy the request out to a webserver
		if *debugWeb != "" {
			debugReverseProxy.ServeHTTP(w, r)
			return
		}

		reqpath := ps.ByName("filepath")
		realpath := strings.TrimLeft(reqpath, "/")
		b, err := Asset(realpath)
		if err != nil {
			log.Debugln("Could not find asset: ", err)
			return
		} else {
			// Get mimetype
			mimetype := mime.TypeByExtension(filepath.Ext(realpath))

			w.Header().Set("Content-Type", mimetype)
			w.Header().Set("Content-Length", fmt.Sprintf("%v",len(b)))
			sha := sha256.New()
			sha.Write(b)
			w.Header().Set("ETag", hex.EncodeToString(sha.Sum(nil)))
			w.Write(b)
		}
	})

	// VNC websocket endpoint
	router.GET("/vnc/:shortname", vncWebSocket(manager))

	// Return a list of known servers as JSON
	router.GET("/api/list", func(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
		servers := manager.List()
		jenc := json.NewEncoder(w)

		w.Header().Set("Content-Type", "application/json")
		jenc.Encode(servers)
	})

	router.GET("/api/list/subscribe", func(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
		conn, err := sse.Upgrade(w, r)
		if err != nil {
			log.Errorln("SSE upgrade failed:", err)
			http.Error(w, "Failed to upgrade connection", 500)
			return
		}
		defer conn.Close()

		ch := manager.Subscribe()
		defer manager.Unsubscribe(ch)

		for e := range ch {
			switch event := e.(type) {
			case addedServer:
				conn.WriteJsonEvent("added", event)
			case removedServer:
				conn.WriteJsonEvent("removed", event)
			}
		}
	})

	var err error
	if *insecure {
		log.Warnln("SSL DISABLED")
		err = http.ListenAndServe(*listen, router)
	} else {
		err = http.ListenAndServeTLS(*listen, *sslCert, *sslKey, router)
	}

	if err != nil {
		log.Fatal(err)
	}
}

func vncWebSocket(manager *serverManager) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		shortname := ps.ByName("shortname")
		var server vncServer

		foundServer := false
		for k, knownServer := range manager.List() {
			if shortname == k {
				foundServer = true
				server = knownServer
				break
			}
		}

		if !foundServer {
			http.Error(w, "VNC host not found", 404)
			return
		}

		log.With("type", server.NetType).
			With("addr",server.Address).
			With("user",server.Username).Infoln("Opening VNC connection to server")

		vncConn, err := net.Dial(server.NetType, server.Address)
		if err != nil {
			log.Errorln("Error connecting to VNC server", err)
			http.Error(w, "Error connecting to VNC server", 500)
			return
		}
		defer vncConn.Close()

		protocols := websocket.Subprotocols(r)
		log.Debugln("Subprotocols Requested:", protocols)
		conn, err := wsupgrader.Upgrade(w, r, http.Header{"Sec-Websocket-Protocol" : {protocols[0]}})
		if err != nil {
			log.Infoln("Websocket Upgrade:", err)
			return
		}
		defer conn.Close()

		log.With("local_addr", conn.LocalAddr()).
			With("remote_addr", conn.RemoteAddr()).Debugln("Websocket online")

		writerExit := make(chan int)
		readerExit := make(chan int)

		// Websocket -> VNC
		go func() {
			// Read loop
			for {
				_, message, err := conn.ReadMessage()
				if err != nil {
					log.Errorln("WEBSOCKET READ:",err)
					break
				}
				_, err = vncConn.Write(message)
				if err != nil {
					log.Errorln("VNC WRITE:",err)
					break
				}
			}
			log.Debugln("Websocket reader finished")
			close(readerExit)
		}()

		// VNC -> websocket
		go func() {
			rbuffer := make([]byte, 1024)
			for {
				numBytes, err := vncConn.Read(rbuffer)
				if err != nil {
					log.Errorln("VNC READ:", err)
					break
				}
				// We originally forgot to specify numBytes, so sent frames of garbage
				err = conn.WriteMessage(websocket.BinaryMessage, rbuffer[:numBytes])
				if err != nil {
					log.Errorln("WEBSOCKET WRITE:", err)
					break
				}
			}
			log.Debugln("Websocket writer finished")
			close(writerExit)
		}()

		// Wait for reader or writer exit
		select {
		case <- writerExit:
			break
		case <- readerExit:
			break
		}
	}
}