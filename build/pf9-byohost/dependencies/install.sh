#!/bin/bash
mkdir -p /var/log/pf9/byoh/
touch /var/log/pf9/byoh/byoh-agent.log

./byoh-hostagent-linux-amd64 --bootstrap-kubeconfig bootstrap-kubeconfig.yaml  > ~/byoh-agent.log 2>&1 & disown -a

ps aux | grep byoh
