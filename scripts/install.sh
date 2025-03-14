command -v imgpkg >/dev/null 2>&1
IMGPKG=$?
if [ "$IMGPKG" -ne 0 ]; then
	echo "imgpkg not present"
	echo "installing imgpkg"
	sudo apt update && sudo apt install -y imgpkg
fi
echo "creating /var/lib/pf9/ dir"
mkdir -p /var/lib/pf9/
cd /var/lib/pf9/
echo "Pulling Platform9 Byohost Packages"
if imgpkg pull -i quay.io/platform9/pf9-byoh:byoh-agent --output /var/lib/pf9/; then 
	echo "Successfully Pulled Byohost Packages"
	echo "installing socat"
	dpkg -i socat.deb
	echo "installing ethtool"
	dpkg -i ethtool.deb
	echo "installing ebtables"
	dpkg -i ebtables.deb
	echo "installing conntrack"
	dpkg -i conntrack.deb
	echo "installing pf9-byohost-agent"
	dpkg -i pf9-byohost-agent.deb 
	echo "Starting Systemd service for Byohost Agent"
	systemctl daemon-reload
	systemctl enable pf9-byohost-agent.service
	echo "pf9-byohost-agent systemd service enabled successfully"
	systemctl start pf9-byohost-agent.service
	echo "pf9-byohost-agent systemd service started successfully"
else 
	echo "Failed to Pull PF9 Byohost Packages  , please check network connection and restart byoctl "
fi
