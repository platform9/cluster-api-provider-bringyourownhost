#!/bin/bash

echo "after pf9-byohost-agent installation"
mkdir -p /var/log/pf9/byoh
mkdir -p /root/.byoh/

touch  /var/log/pf9/byoh/byoh-agent.log
if [ -f /binary/pf9-byoh-hostagent-linux-amd64 ]; then
	    chmod +x /binary/pf9-byoh-hostagent-linux-amd64
	else
	    echo "Error: Binary file not found at /binary/pf9-byoh-hostagent-linux-amd64"
	    exit 1
	fi

if [ -f /lib/systemd/system/pf9-byohost-agent.service ]; then
	    if ! cp /lib/systemd/system/pf9-byohost-agent.service /etc/systemd/system/pf9-byohost-agent.service; then
	        echo "Error: Failed to copy service file"
	        exit 1
	    fi
	else
	    echo "Error: Service file not found at /lib/systemd/system/pf9-byohost-agent.service"
	    exit 1
	fi

mkdir -p /etc/pf9-byohost-agent.service.d/
touch /etc/pf9-byohost-agent.service.d/pf9-byohost-agent.conf
export NAMESPACE=$(grep 'namespace: *' /root/.byoh/config | awk '{print $2}')

echo "NAMESPACE=$NAMESPACE" > /etc/pf9-byohost-agent.service.d/pf9-byohost-agent.conf
echo "BOOTSTRAP_KUBECONFIG=/etc/pf9-byohost-agent.service.d/bootstrap-kubeconfig.yaml" >> /etc/pf9-byohost-agent.service.d/pf9-byohost-agent.conf 

systemctl daemon-reload
systemctl enable pf9-byohost-agent.service
systemctl start pf9-byohost-agent.service
