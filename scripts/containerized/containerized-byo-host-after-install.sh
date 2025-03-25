#start systemd service for containerized byo-agent HA

echo "After containerized-byo-agent installation "
mkdir -p /var/log/pf9/byoh
touch  /var/log/pf9/byoh/byoh-agent.log


if [ -f /lib/systemd/system/containerized-byo-agent.service ]; then
	    if ! cp /lib/systemd/system/containerized-byo-agent.service /etc/systemd/system/containerized-byo-agent.service; then
	        echo "Error: Failed to copy service file"
	        exit 1
	    fi
	else
	    echo "Error: Service file not found at /lib/systemd/system/containerized-byo-agent.service"
	    exit 1
	fi



echo "systemctl daemon reload\n" && systemctl daemon-reload
echo "Enabling systemd service for containerized-byo-agent\n" && systemctl enable containerized-byo-agent.service
echo "Starting systemd service for containerized-byo-agent\n" && systemctl start containerized-byo-agent.service

