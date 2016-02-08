## novncgo - a go wrapper for noVNC

novncgo is a web application that supports remote access to multiple computers
from a single web-application. Users are logged in with a password and may
connect to a list of VNC-servers in the LAN using the [noVNC](http://kanaka.github.com/noVNC)
Javascript VNC client.

### Supported browsers

* Modern Firefox
* Chrome

### Building

git, hg, golang.

     make

### Setting up

Build a file of passwords (called novncgo.passwd):

    novncgo-passwd/novncgo-passwd novncgo.passwd <my-username>

Build a file of servers,

    cp novncgo.servers.example novncgo.servers
    vi novncgo.servers

Add a user called novncgo.

And finally run it:

    (cd /path/to/novncgo && su -c "nohup ./novncgo --hostname=foobar.example.com > log" novncgo &)

