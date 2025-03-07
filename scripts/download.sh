mkdir -p dep && cd dep
apt-get download {socat,ethtool,ebtables,conntrack}
mv *socat* socat.deb
mv *ebtables* ebtables.deb
mv *ethtool* ethtool.deb
mv *conntrack* conntrack.deb
cp /root/pf9-byohost-agent.deb .
cd ..
imgpkg push -f dep/ -i snhpf9/byoh-bundle:v1.0
