// go:generate go-bindata -ignore '.git/.*' -prefix static static/...
package main

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/JanBerktold/sse"
	"github.com/gorilla/websocket"
	"github.com/julienschmidt/httprouter"
	"github.com/kardianos/osext"
	"github.com/prometheus/common/log"
	"gopkg.in/fsnotify.v1"
	"mime"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var defaultConfigDir = "."

var (
	hostname, _ = os.Hostname()
	exePath, _  = osext.Executable()
	exeName     = path.Base(exePath)

	fileDir = flag.String("filedir", "export", "Directory for exported files")

	listen  = flag.String("listen.addr", ":6080", "Location to listen for connections")
	host    = flag.String("listen.hostname", hostname, "Hostname to use for certificate")
	sslCert = flag.String("listen.ssl.cert", exeName+"."+hostname+".crt", "Path to SSL certfile. Will be generated if it does not exist.")
	sslKey  = flag.String("listen.ssl.key", exeName+"."+hostname+".key", "Path to SSL keyfile. Will be generated if it does not exist.")

	insecure = flag.Bool("listen.ssl.disable", false, "Disable SSL entirely (INSECURE!)")

	socketPaths = flag.String("servers.watch-glob", "", "Glob path to watch for VNC UNIX socket servers appearing")
	watchPollInterval = flag.Duration("servers.watch-interval", time.Second * 5, "If no inotify events in this long, manually poll the watch paths. 0 disables.")

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
type VNCServer interface {
	String() string
	Short() string
}

type vncServer struct {
	NetType  string `json:"nettype"`  // Golang network type
	Address  string `json:"address"`  // Address
	Username string `json:"username"` // Username
	Password string `json:"-"`        // Password
}

// Types used for publishing server events
type ManagerActionType string

const (
	Manager_AddedServer   ManagerActionType = "added"
	Manager_RemovedServer ManagerActionType = "removed"
)

type ManagerAction struct {
	action ManagerActionType
	server VNCServer
}

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

	if urlp.Path != "" { // file-likes
		return vncServer{
			NetType:  urlp.Scheme,
			Address:  urlp.Path,
			Username: user,
			Password: password,
		}
	} else { // actual network sockets
		return vncServer{
			NetType:  urlp.Scheme,
			Address:  urlp.Host,
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

// Short representation for the manager and access paths.
// This is just the MD5 hash of the URL form representation, in hex
func (this vncServer) Short() string {
	hasher := md5.New()
	hasher.Write([]byte(this.String()))

	return hex.EncodeToString(hasher.Sum(nil))
}

// Maintains the list of currently available VNC files
type serverManager struct {
	availableServers map[string]vncServer
	subscribers      []chan ManagerAction // Subscribers requesting server updates
	mtx              sync.RWMutex
	smtx             sync.Mutex
}

// Request a channel that publishes updates
func (this *serverManager) Subscribe() chan ManagerAction {
	this.smtx.Lock()
	defer this.smtx.Unlock()

	ch := make(chan ManagerAction, 1)
	this.subscribers = append(this.subscribers, ch)
	return ch
}

func (this *serverManager) Unsubscribe(ch chan ManagerAction) {
	this.smtx.Lock()
	defer this.smtx.Unlock()

	for idx, kch := range this.subscribers {
		if ch == kch {
			this.subscribers = append(this.subscribers[:idx], this.subscribers[idx+1:]...)
			break
		}
	}
}

func (this *serverManager) publish(action ManagerActionType, server VNCServer) {
	for _, ch := range this.subscribers {
		// Always send messages
		select {
		case ch <- ManagerAction{action, server}:
			continue
		default:
			log.Infoln("Dropping message due to full channel")
		}
	}
}

// Add a server to the list
func (this *serverManager) Add(server vncServer) {
	this.mtx.Lock()
	defer this.mtx.Unlock()

	_, ok := this.availableServers[server.Short()]

	// Don't duplicate server publications
	if !ok {
		log.With("server_shortpath", server.Short()).With("server", server.String()).Infoln("Adding server")
		this.availableServers[server.Short()] = server
		this.publish(Manager_AddedServer, server)
	} else {
		log.With("server_shortpath", server.Short()).With("server", server.String()).Debugln("Ignoring already added server")
	}
}

// Remove a server from the list by it's network type and address
func (this *serverManager) RemoveByAddress(address string) {
	this.mtx.Lock()
	defer this.mtx.Unlock()

	log.Infoln("Removing server:", address)

	toRemove := []string{}
	for k, v := range this.availableServers {
		if v.Address == address {
			toRemove = append(toRemove, k)
		}
	}

	for _, k := range toRemove {
		this.publish(Manager_RemovedServer, this.availableServers[k])
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

func pollSocketDirectory(glob string, manager *serverManager) {
	watchPaths, err := filepath.Glob(glob)
	if err != nil {
		log.Fatalln("Error globbing watch-dirs:", err)
	}

	for _, globPath := range watchPaths {
		// Add existent files to the manager
		if s, err := os.Stat(globPath); !s.Mode().IsDir() && !os.IsNotExist(err) {
			server := vncServer{
				NetType: "unix",
				Address: globPath,
			}
			manager.Add(server)
		}
	}
}

func handleSocketDirectoryEvent(glob string, manager *serverManager, e fsnotify.Event) {
	// Check the path against the glob (which should generally filter for something like
	// socket.
	matched, err := filepath.Match(glob, e.Name)
	if err != nil {
		log.Error("Filepath globber error:", err)
	}
	if matched {
		switch e.Op {
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
			log.Debugln("Ignoring Op:", e.String())
		}
	}
}

func watchSocketFiles(events <-chan fsnotify.Event, glob string, manager *serverManager) {
	// Do an initial sweep of the glob path to pickup existing files. Run async so we don't
	// miss events.

	// Glob the watch paths, reduce to minimal set and establish watches.
	if glob != "" {
		log.Debugln("Watch glob supplied:", glob)
		watchPaths, err := filepath.Glob(glob)
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

	// Poll the socket directory to ensure we populate initial matches immediately
	go pollSocketDirectory(glob, manager)

	// Loop into the socket watcher
	log.Infoln("Socket watch loop Started")
	func() {
		for {
			// Watchdog poller for sockets
			var intervalTimeoutCh <-chan time.Time
			if *watchPollInterval != 0 {
				intervalTimeoutCh = time.After(*watchPollInterval)
			} else {
				intervalTimeoutCh = make(<-chan time.Time)
			}
			select {
			case e, ok := <-events:
				log.Debugln("Inotify Event:", e.Op, e.Name)
				handleSocketDirectoryEvent(glob, manager, e)
				if !ok {
					log.Infoln("Watch files process finishing.")
					return
				}
			case <-intervalTimeoutCh:
				log.Debugln("Watch poll timeout: doing manual poll")
				pollSocketDirectory(glob, manager)
			}
		}
	}()
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
		debugUrl, err := url.Parse(*debugWeb)
		if err != nil {
			log.Fatalln("Invalid debug proxy url:", err)
		}
		log.Infoln("Proxy debugging enabled to", *debugWeb)
		debugReverseProxy = httputil.NewSingleHostReverseProxy(debugUrl)
	}

	// Index endpoint
	router.GET("/", func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		http.Redirect(w, r, "/static/dashboard.html", 302)
	})

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
			log.Warnln("Could not find asset: ", err)
			return
		} else {
			// Get mimetype
			mimetype := mime.TypeByExtension(filepath.Ext(realpath))

			w.Header().Set("Content-Type", mimetype)
			w.Header().Set("Content-Length", fmt.Sprintf("%v", len(b)))
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

		log.Debugln("New subcriber:", r.RemoteAddr)

		timeCh := time.Tick(time.Second)
		func() {
			for {
				select {
				case e := <-ch:
					err = conn.WriteStringEvent(string(e.action), e.server.Short())
					if err != nil {
						return
					}
				case <-timeCh:
					if !conn.IsOpen() {
						return
					}
				}
			}
		}()

		log.Debugln("Subscriber finished:", r.RemoteAddr)
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
			With("addr", server.Address).
			With("user", server.Username).Infoln("Opening VNC connection to server")

		vncConn, err := net.Dial(server.NetType, server.Address)
		if err != nil {
			log.Errorln("Error connecting to VNC server", err)
			http.Error(w, "Error connecting to VNC server", 500)
			return
		}
		defer vncConn.Close()

		protocols := websocket.Subprotocols(r)
		log.Debugln("Subprotocols Requested:", protocols)
		conn, err := wsupgrader.Upgrade(w, r, http.Header{"Sec-Websocket-Protocol": {protocols[0]}})
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
					log.Errorln("WEBSOCKET READ:", err)
					break
				}
				_, err = vncConn.Write(message)
				if err != nil {
					log.Errorln("VNC WRITE:", err)
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
		case <-writerExit:
			break
		case <-readerExit:
			break
		}
	}
}
