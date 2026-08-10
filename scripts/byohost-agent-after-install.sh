#!/bin/bash

echo "after byohost-agent installation"
mkdir -p /var/log/byoh
mkdir -p /root/.byoh/

touch  /var/log/byoh/byoh-agent.log
if [ -f /binary/byoh-hostagent-linux-amd64 ]; then
	    chmod +x /binary/byoh-hostagent-linux-amd64
	else
	    echo "Error: Binary file not found at /binary/byoh-hostagent-linux-amd64"
	    exit 1
	fi

if [ -f /lib/systemd/system/byohost-agent.service ]; then
	    if ! cp /lib/systemd/system/byohost-agent.service /etc/systemd/system/byohost-agent.service; then
	        echo "Error: Failed to copy service file"
	        exit 1
	    fi
	else
	    echo "Error: Service file not found at /lib/systemd/system/byohost-agent.service"
	    exit 1
	fi

mkdir -p /etc/byohost-agent.service.d/
touch /etc/byohost-agent.service.d/byohost-agent.conf
export NAMESPACE=$(grep 'namespace: *' /root/.byoh/config | awk '{print $2}')
export REGION=$(cat /root/.byoh/region)

echo "NAMESPACE=$NAMESPACE" > /etc/byohost-agent.service.d/byohost-agent.conf
echo "BOOTSTRAP_KUBECONFIG=/etc/byohost-agent.service.d/bootstrap-kubeconfig.yaml" >> /etc/byohost-agent.service.d/byohost-agent.conf
echo "REGION=$REGION" >> /etc/byohost-agent.service.d/byohost-agent.conf

systemctl daemon-reload
systemctl enable byohost-agent.service
systemctl start byohost-agent.service
