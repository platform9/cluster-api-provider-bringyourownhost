#!/bin/bash

echo "after pf9-byohost-agent installation"

mkdir -p /var/log/pf9/byoh
touch  /var/log/pf9/byoh/byoh-agent.log
chmod +x /binary/pf9-byoh-hostagent-linux-amd64
cp /lib/systemd/system/pf9-byohost-agent.service  /etc/systemd/system/pf9-byohost-agent.service

if [ -z "$BOOTSTRAP_KUBECONFIG" ]; then
    echo "Error: BOOTSTRAP_KUBECONFIG environment variable is not set."
    exit 1
fi

if [ ! -f "$BOOTSTRAP_KUBECONFIG" ]; then
    echo "Error: File specified in "$BOOTSTRAP_KUBECONFIG" does not exist: $BOOTSTRAP_KUBECONFIG"
    exit 1
fi

systemctl daemon-reload
systemctl enable pf9-byohost-agent.service
systemctl start pf9-byohost-agent.service

