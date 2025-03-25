#!/bin/bash

# Exit immediately if a command fails
set -e

LOG_FILE="/var/log/pf9/byoh-agent-uninstall.log"

echo "Starting uninstallation of containerized-byo-agent..." | tee -a "$LOG_FILE"

# Stop and disable the systemd service
echo "Stopping and disabling containerized-byo-agent service..." | tee -a "$LOG_FILE"
if systemctl stop containerized-byo-agent.service >> "$LOG_FILE" 2>&1; then
    echo "Service stopped successfully" | tee -a "$LOG_FILE"
else
    echo "WARNING: Failed to stop the service or it may not be running" | tee -a "$LOG_FILE"
fi

systemctl disable containerized-byo-agent.service >> "$LOG_FILE" 2>&1 || echo "Service was already disabled" | tee -a "$LOG_FILE"

# Reload systemd daemon
systemctl daemon-reload >> "$LOG_FILE" 2>&1
echo "Systemd daemon reloaded" | tee -a "$LOG_FILE"

# Remove service file
if [ -f /etc/systemd/system/containerized-byo-agent.service ]; then
    echo "Removing service file..." | tee -a "$LOG_FILE"
    rm -f /etc/systemd/system/containerized-byo-agent.service
    echo "Service file removed successfully" | tee -a "$LOG_FILE"
else
    echo "Service file already removed or not found" | tee -a "$LOG_FILE"
fi

# Remove log files
if [ -f /var/log/pf9/byoh/byoh-agent.log ]; then
    echo "Removing log files..." | tee -a "$LOG_FILE"
    rm -f /var/log/pf9/byoh/byoh-agent.log
    echo "Log files removed successfully" | tee -a "$LOG_FILE"
else
    echo "Log files already removed or not found" | tee -a "$LOG_FILE"
fi
echo "Uninstallation of containerized-byo-agent completed successfully" | tee -a "$LOG_FILE"

