
var evtSource = new EventSource("/api/list/subscribe");
var vncSessions = {};

var host, port;

function updateState(rfb, state, oldstate, msg) {
    console.log(state);
    if (state == 'disconnected') {
        rfb.connect(rfb._rfb_host, rfb._rfb_port, rfb._rfb_password, rfb._rfb_path)
    }
}

function newVNCClient(path, canvas) {
    console.log("Creating VNC client: " + path);
    try {
        rfb = new RFB({'target':       canvas,
            'encrypt':      WebUtil.getConfigVar('encrypt',
                (window.location.protocol === "https:")),
            'repeaterID':   WebUtil.getConfigVar('repeaterID', ''),
            'true_color':   WebUtil.getConfigVar('true_color', true),
            'local_cursor': WebUtil.getConfigVar('cursor', true),
            'shared':       WebUtil.getConfigVar('shared', true),
            'view_only':    WebUtil.getConfigVar('view_only', false),
            'onUpdateState':  updateState,
            'onXvpInit':    null,
            'onPasswordRequired':  null,
            'onFBUComplete': null});
    } catch (exc) {
        console.log('Unable to create RFB client -- ' + exc);
        return; // don't continue trying to connect
    }

    rfb.connect(host, port, "", path);

    vncSessions[path] = [ canvas, rfb ];
}

function newVNCHost(e) {
    t = vncSessions["vnc/" + e.data]
    if (t == "undefined") {
        console.log("Already have a session to" + e.data);
        return
    }

    canvas = document.createElement("canvas");
    canvas.id = e.data; // Get the data

    document.body.appendChild(canvas);

    // Instantiate a new VNC client
    newVNCClient("vnc/" + e.data, canvas);

}

function removedVNCHost(e) {
    console.log("Removing VNC client: " + e.data);
    t = vncSessions["vnc/" + e.data]
    if (t == "undefined") {
        console.log("Missing session, doing nothing for: " + e.data);
        return
    }
    canvas = t[0];
    rfb = t[1];

    rfb.disconnect();
    document.body.removeChild(canvas);
    delete vncSessions["vnc/" + e.data];
}

function runApp() {
    Util.load_scripts(["webutil.js", "base64.js", "websock.js", "des.js",
        "keysymdef.js", "keyboard.js", "input.js", "display.js",
        "inflator.js", "rfb.js", "keysym.js"]);
}

function loadRunningVncs() {
    // Pickup any servers we might've missed
    var req = new XMLHttpRequest();
    req.addEventListener("load", function() {
        try {
            vncList = JSON.parse(req.responseText)
            for (var property in vncList) {
                if (vncList.hasOwnProperty(property)) {
                    newVNCHost({ 'data' : property });
                }
            }
        } catch (exc) {
            console.log("Failed parsing response text. Will retry in 1s.")
            setTimeout(loadRunningVncs, 1000)
        }
    });
    req.open("GET", "/api/list", true);
    req.send();
}

window.onscriptsload = function () {
    WebUtil.init_logging(WebUtil.getConfigVar('logging', 'warn'));

    // By default, use the host and port of server that served this file
    host = WebUtil.getConfigVar('host', window.location.hostname);
    port = WebUtil.getConfigVar('port', window.location.port);

    // if port == 80 (or 443) then it won't be present and should be
    // set manually
    if (!port) {
        if (window.location.protocol.substring(0,5) == 'https') {
            port = 443;
        }
        else if (window.location.protocol.substring(0,4) == 'http') {
            port = 80;
        }
    }

    evtSource.addEventListener("added", newVNCHost, false);
    evtSource.addEventListener("removed", removedVNCHost, false);

    loadRunningVncs();
}

document.onreadystatechange = function () {
    if (document.readyState == "complete") {
        runApp();
    }
};