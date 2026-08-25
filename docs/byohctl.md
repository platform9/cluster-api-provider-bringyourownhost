# byohctl

`byohctl` (`cmd/byohctl`) is a CLI that automates a BYOH host's lifecycle: onboarding, deauthorising,
and decommissioning. Today, its primary `onboard` flow authenticates against a Platform9
management plane to do that — a coupling that's considered a mistake and is expected to go away
as `byohctl` moves toward being vendor-neutral. Treat that dependency as an implementation detail
of the current `onboard` flow, not as what `byohctl` is for.

## Commands

- `byohctl onboard` - authenticate, install the agent, and register the host.
- `byohctl deauthorise` - deauthorise a host from its byo cluster.
- `byohctl decommission` - decommission a host from the management cluster.
- `byohctl version` - print the version information.

Run `byohctl <command> --help` for the full flag list of each.

## Installation

`byohctl` ships inside the agent deb/rpm package (currently named `pf9-byohost-agent`; expected to
be renamed to drop the `pf9-` prefix), at `/usr/bin/byohctl`. Installing or upgrading that package
is what keeps `byohctl` itself current — there's no separate `byohctl upgrade` step.

### If you already have the agent package installed

`byohctl` is already on your `PATH` at `/usr/bin/byohctl`. Just run `byohctl <command>`.

### If you're bootstrapping a host that doesn't have the package yet

You'll typically obtain a standalone `byohctl` binary to run `byohctl onboard`, which installs the
agent package (and, from that point on, `byohctl` itself) for you. To make sure this initial copy
gets superseded by the package-managed one instead of quietly shadowing it forever, install it to
the canonical path before running it, rather than invoking it from wherever it was downloaded:

```shell
chmod +x ./byohctl
sudo install -m 0755 ./byohctl /usr/bin/byohctl
sudo byohctl onboard ...
```

From then on, always invoke it as `byohctl` (resolved via `PATH`), not by its download path (e.g.
`./byohctl`) or a copy kept elsewhere. `onboard`'s package install will overwrite
`/usr/bin/byohctl` in place on every future upgrade; a copy anywhere else never gets that update
and will silently drift out of date.

If `byohctl` ever detects it isn't running from `/usr/bin/byohctl`, it warns — worth acting on
before relying on that copy further. If nothing is installed at the canonical path yet, the
warning names the exact `install` command to fix it. If something's already there, it never
suggests overwriting it: that copy is package-managed and stays current through agent package
upgrades regardless of which binary happens to carry a newer version string, so the warning
instead tells you to use it. This warning is skipped for
`onboard` itself, since a successful `onboard` run installs the canonical copy for you; it applies
to every other command (`deauthorise`, `decommission`, `version`).
