#!/bin/bash
# Script to set up systemd service for BYOH agent

BYOH_DIR="$1"
AGENT_BINARY="$2"
NAMESPACE="$3"
LOG_DIR="$BYOH_DIR/logs"
AGENT_LOG="$LOG_DIR/agent.log"

# Create systemd service file
cat > /etc/systemd/system/byoh-agent.service << EOF
[Unit]
Description=BYOH Agent Service
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=$BYOH_DIR
ExecStart=$AGENT_BINARY --namespace $NAMESPACE --metricsbindaddress 0 --v 4
Restart=on-failure
RestartSec=5s
StandardOutput=append:$AGENT_LOG
StandardError=append:$AGENT_LOG

[Install]
WantedBy=multi-user.target
EOF

# Enable and start the service
systemctl daemon-reload
systemctl enable byoh-agent.service
systemctl start byoh-agent.service
