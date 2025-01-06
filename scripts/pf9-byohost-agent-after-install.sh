#!/bin/bash

echo "after pf9-byohost-agent installation"
mkdir -p /var/log/pf9/byoh
mkdir -p /root/.byoh/

touch  /var/log/pf9/byoh/byoh-agent.log
chmod +x /binary/pf9-byoh-hostagent-linux-amd64

cp /lib/systemd/system/pf9-byohost-agent.service  /etc/systemd/system/pf9-byohost-agent.service


mkdir -p /etc/pf9-byohost-agent.service.d/
touch /etc/pf9-byohost-agent.service.d/pf9-byohost-agent.conf
export NAMESPACE=$(grep 'namespace: *' /root/.byoh/config | awk '{print $2}')

echo "NAMESPACE=$NAMESPACE" > /etc/pf9-byohost-agent.service.d/pf9-byohost-agent.conf
echo "BOOTSTRAP_KUBECONFIG=/etc/pf9-byohost-agent.service.d/bootstrap-kubeconfig.yaml" >> /etc/pf9-byohost-agent.service.d/pf9-byohost-agent.conf 

# systemctl daemon-reload
# systemctl enable pf9-byohost-agent.service
# systemctl start pf9-byohost-agent.service
