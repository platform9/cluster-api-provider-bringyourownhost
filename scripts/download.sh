sudo mkdir -p dep && cd dep
sudo apt install imgpkg
sudo apt-get download {socat,ethtool,ebtables,conntrack}
sudo mv *socat* socat.deb
sudo mv *ebtables* ebtables.deb
sudo mv *ethtool* ethtool.deb
sudo mv *conntrack* conntrack.deb
sudo cp /root/pf9-byohost-agent.deb .
cd ..
sudo imgpkg push -f dep/ -i snhpf9/byoh-bundle:v1.0
