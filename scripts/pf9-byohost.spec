Name:           pf9-byohost
Summary:        Platform9 Kubernetes ByohAgent
License:        Commercial
URL:            http://www.platform9.net
Provides:       pf9-byohost
Provides:       pf9app
Requires:       socat
Requires:       ebtables
Requires:       ethtool
Requires:       conntrack
AutoReqProv:    no

%global __os_install_post %(echo '%{__os_install_post}' | sed -e 's!/usr/lib[^[:space:]]*/brp-python-bytecompile[[:space:]].*$!!g')

%description
Platform9 Kubernetes ByohAgent

%prep

%build

%install
SRC_DIR=%_src_dir

rm -fr $RPM_BUILD_ROOT
mkdir -p $RPM_BUILD_ROOT
cp -r $SRC_DIR/* $RPM_BUILD_ROOT
mkdir -p $RPM_BUILD_ROOT/var/log/pf9/byoh
chmod +x $RPM_BUILD_ROOT/binary/pf9-byoh-hostagent-linux-amd64
cp $RPM_BUILD_ROOT/lib/systemd/system/pf9-byohost-agent.service $RPM_BUILD_ROOT/etc/systemd/system/pf9-byohost-agent.service




%clean
rm -rf $RPM_BUILD_ROOT

%files
%defattr(-,root,root,-)
%attr(0644, root, root) /binary/pf9-byoh-hostagent-linux-amd64
/binary/pf9-byoh-hostagent-linux-amd64
/lib/systemd/system/pf9-byohost-agent.service
/namespace

%pre
# Set a flag indicating whether the package check should be performed
perform_package_check=true

if [[ $(grep "rocky" /etc/os-release) ]]; then
    perform_package_check=false
    echo "It is Rocky Linux, Libcgroup-tools package is not needed"
fi

# Check if the package check should be performed
if [[ "$perform_package_check" == "true" ]]; then
    # Check if libcgroup-tools is installed
    if ! rpm -q libcgroup-tools > /dev/null; then
        echo "Libcgroup-tools package is not installed. Aborting installation."
        exit 1
    fi
fi

%post

echo "after pf9-byohost-agent installation"

mkdir -p /var/log/pf9/byoh
touch  /var/log/pf9/byoh/byoh-agent.log
touch /var/log/pf9/byoh/byoh-agent-uninstall.log
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

%preun

# Exit immediately if a command fails
set -e

LOG_FILE="/var/log/pf9/byoh/byoh-agent-uninstall.log"

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

echo "Uninstallation of pf9-byoh-hostagent completed successfully" | tee -a "$LOG_FILE"


%postun
echo "Post removal script of pf9-BYOHOST-agent package"

%changelog
