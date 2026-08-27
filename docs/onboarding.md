# BYOH Onboarding Flow

## TOKENS/SECRETS/CERTS involved

1. **dex-client-secret** (needed for /dex/token)
   - What it can access?
     - can only be used for /dex/token call
2. **dex jwt token** (used to get SA-token based kubeconfig stored in the secret in the tenant namespace)
   - What it can access?
     - has access to all capi crs (cluster, machine, md, osm etc) in the tenant namespace
     - can access secrets, cms in the tenant namespace
3. **SA-token based kubeconfig** - stored at `~/.byoh/config`
   - What it can access?
     - can access below resources from the specific tenant ns where the host is onboarded
     - Role - csr, svc, cm, secrets, byohost, machine, md, pods, serviceaccounts, endpoints, events, ingress, roles, rolebindings
     - ClusteRole - csr, byohost
4.  **cert based kubeconfig** with subject CN: byoh:host:<hostname> O:byoh:hosts -
   - What it can access?
   - (byoh-admission-controller in picture which gates this?)
   - (This one is never generated in our flow today)

## FLOW -

1. byohctl download from s3 - public, no token required
2. byohctl onboard with user creds and dex-client-secret
   1. checks if bootstrap-kubeconfig flag is set
      1. if yes -
         copies that file to `~/.byoh/config`
      2. if no - we generate download SA-token based kubeconfig from mgmt plane -
         1. gets the dex token (using user creds)
         2. gets the sa-token kubeconfig from the mgmt plane tenant namespace (by using dex token) and saves at the `~/.byoh/config`
   2. installs agent-deb pkg
      1. contains systemd service with -
         ```
         ExecStart=/bin/bash -c "/binary/pf9-byoh-hostagent-linux-amd64 --bootstrap-kubeconfig \"$BOOTSTRAP_KUBECONFIG\"
         --namespace \"$NAMESPACE\" --label \"$REGION\" >> /var/log/pf9/byoh/byoh-agent.log 2>&1"
         ```
      2. the after-install.sh script of deb pkg sets the `BOOTSTRAP_KUBECONFIG=/etc/pf9-byohost-agent.service.d/bootstrap-kubeconfig.yaml` (hardcoded)
         in the `/etc/pf9-byohost-agent.service.d/pf9-byohost-agent.conf` (along with the NAMESPACE and REGION)
3. host-agent-binary process starts (managed via systemd service)
   1. main.go - checks if [ bootstrapKubeConfig != "" && (`~/.byoh/config` doesn't exist) ] - then
      1. creates a cert-based kubeconfig using CSR flow.
      2. stores the generated cert based kubeconfig at `~/.byoh/config` (overwriting SA-token based kubeconfig)
   2. uses `~/.byoh/config` to get k8s client and regsisters byoh host in the mgmt plane
   3. rotates the cert if less than 20% time remains
   4. starts host reconciler

## Observations -

### Whats expected -

1. Expects the bootstrap-kubeconfig file at the path provided by --bootstrap-kubeconfig to the agent binary
2. Run CSR flow to get CERT based kubeconfig and store at `~/.byoh/config` - which will be used to register and reconciler
3. `~/.byoh/config` is the expected path which should have final cert-based config

### What we are doing -

1. We are pointing --bootstrap-kubeconfig to /etc/pf9-byohost-agent.service.d/bootstrap-kubeconfig.yaml
   1. This file doesnt exist in our flow.
   2. we should copy the sa-token based config at this location.
2. **CSR flow is skipped** - We store SA-token based kubeconfig at `~/.byoh/config` instead of cert based. hence **CSR flow is skipped and we never use cert based kubeconfig**. We are using SA-token based kubeconfig as final kubeconfig to the reconciler.

### What we are doing wrong -

1. The downloaded SA-token based kubeconfig should be used as the --bootstrap-kubeconfig and we should not store it at `~/.byoh/config`

## Notes -

1. dex token is never used to register host -
   its only used to download the already generated SA-token based kubeconfig in mgmt plane tenant ns.
