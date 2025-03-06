mkdir -p dep && cd dep
apt-get download {socat,ethtool,ebtables,conntrack}
mv *socat* socat.deb
mv *ebtables* ebtables.deb
mv *ethtool* ethtool.deb
mv *conntrack* conntrack.deb
