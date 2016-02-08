novncgo: noVNC
	go build
	cd novncgo-passwd && go build

clean:
	go clean
	cd novncgo-passwd && go clean

noVNC:
	git clone https://github.com/kanaka/noVNC.git

update: noVNC
	hg pull -u
	cd noVNC && git pull

