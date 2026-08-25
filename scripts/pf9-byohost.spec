Name:           pf9-byohost
Version:        1.0
Release:        %{_buildnum}.git%{_githash}
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
# Don't distribute debug symbols.
%global _build_id_links none

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
chmod +x $RPM_BUILD_ROOT/binary/pf9-byoh-hostagent
chmod +x $RPM_BUILD_ROOT/usr/bin/byohctl
%clean
rm -rf $RPM_BUILD_ROOT

%files
%defattr(-,root,root,-)
%attr(0644, root, root) /binary/pf9-byoh-hostagent
/binary/pf9-byoh-hostagent
/etc/systemd/system/pf9-byohost-agent.service
%attr(0755, root, root) /usr/bin/byohctl
/usr/bin/byohctl

%pre
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

%post -p /bin/bash -f %{_scripts_dir}/pf9-byohost-agent-after-install.sh

%preun -p /bin/bash -f %{_scripts_dir}/pf9-byohost-agent-before-remove.sh

%postun -p /bin/bash -f %{_scripts_dir}/pf9-byohost-agent-after-remove.sh

%changelog
