#!/bin/bash
# BYOH Agent wrapper script for systemd service
export HOME=$1
export KUBECONFIG=$2
cd $1
exec $3 --namespace $4 --metricsbindaddress 0 --v 4
