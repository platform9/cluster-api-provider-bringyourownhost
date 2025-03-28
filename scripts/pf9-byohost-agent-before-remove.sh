#!/bin/bash

# Exit immediately if a command fails
set -e

LOG_FILE="/var/log/pf9/byoh-agent-uninstall.log"

echo "Starting uninstallation of pf9-byoh-hostagent..." | tee -a "$LOG_FILE"

# Attempt to stop the agent using the binary
if /binary/pf9-byoh-hostagent-linux-amd64 phases stop --force --skip-pre-check >> "$LOG_FILE" 2>&1; then
    echo "pf9-byoh-hostagent stopped successfully before uninstallation" | tee -a "$LOG_FILE"
else
    echo "WARNING: pf9-byoh-hostagent could not be stopped before uninstallation" | tee -a "$LOG_FILE"
fi

# Stop and disable the systemd service
echo "Stopping and disabling pf9-byoh-hostagent service..." | tee -a "$LOG_FILE"
if systemctl stop pf9-byohost-agent.service >> "$LOG_FILE" 2>&1; then
    echo "Service stopped successfully" | tee -a "$LOG_FILE"
else
    echo "WARNING: Failed to stop the service or it may not be running" | tee -a "$LOG_FILE"
fi

systemctl disable pf9-byohost-agent.service >> "$LOG_FILE" 2>&1 || echo "Service was already disabled" | tee -a "$LOG_FILE"

# Reload systemd daemon
systemctl daemon-reload >> "$LOG_FILE" 2>&1
echo "Systemd daemon reloaded" | tee -a "$LOG_FILE"

# Remove binary
if [ -f /binary/pf9-byoh-hostagent-linux-amd64 ]; then
    echo "Removing binary..." | tee -a "$LOG_FILE"
    rm -f /binary/pf9-byoh-hostagent-linux-amd64
    echo "Binary removed successfully" | tee -a "$LOG_FILE"
else
    echo "Binary already removed or not found" | tee -a "$LOG_FILE"
fi

# Remove service file
if [ -f /etc/systemd/system/pf9-byohost-agent.service ]; then
    echo "Removing service file..." | tee -a "$LOG_FILE"
    rm -f /etc/systemd/system/pf9-byohost-agent.service
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

if [ -f /etc/pf9-byohost* ]; then
	echo "Removing Pf9 conf files" | tee -a "$LOG_FILE"
	rm -f /etc/pf9-byohost*
	echo "conf files Removed Successfully" | tee -a "$LOG_FILE"
else 
	echo "Conf files already removed or not found " | tee -a "$LOG_FILE"
fi

if [ -f /root/.byoh/config ]; then
    echo "Removing Config File" | tee -a "$LOG_FILE"
    rm -f /root/.byoh/config
    echo "Config file removed successfully" | tee -a "$LOG_FILE"
else
    echo "Config file already removed or not found" | tee -a "$LOG_FILE"
fi

if [ -d /root/.byoh/packages ]; then
    echo "Removing packages directory..." | tee -a "$LOG_FILE"
    rm -rf /root/.byoh/packages 
    echo "packages dir removed successfully" | tee -a "$LOG_FILE"
else
    echo "packages already removed or not found" | tee -a "$LOG_FILE"
fi

if [ -d /root/.byoh ]; then
    echo "Removing .byoh directory..." | tee -a "$LOG_FILE"
    rm -rf /root/.byoh
    echo ".byoh dir removed successfully" | tee -a "$LOG_FILE"
else
    echo ".byoh already removed or not found" | tee -a "$LOG_FILE"
fi

echo " | not removed dependencies              |" | tee -a "$LOG_FILE" 
echo " | socat conntrack ebtables and ethtools |" | tee -a "$LOG_FILE"
echo " | remove it according to your need      |" | tee -a "$LOG_FILE"

if [ -f /usr/bin/byohctl ]; then
    echo "Removing byohctl..." | tee -a "$LOG_FILE"
    rm /usr/bin/byohctl 
    echo "byohctl removed successfully" | tee -a "$LOG_FILE"
else
    echo "byohctl already removed or not found" | tee -a "$LOG_FILE"
fi

echo "Uninstallation of pf9-byoh-hostagent completed successfully" | tee -a "$LOG_FILE"

