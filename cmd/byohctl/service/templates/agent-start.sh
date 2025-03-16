#!/bin/bash
# BYOH Agent startup script
export HOME=$1
cd $1
$2 --namespace $3 --metricsbindaddress 0 > $4 2>&1 &
echo $! > $1/agent.pid
exit 0
