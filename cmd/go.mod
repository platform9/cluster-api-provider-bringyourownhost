module github.com/platform9/cluster-api-provider-bringyourownhost/cmd

go 1.24

toolchain go1.24.0

require (
	github.com/coreos/go-systemd/v22 v22.5.0
	github.com/spf13/cobra v1.7.0
	golang.org/x/term v0.29.0
)

require golang.org/x/sys v0.30.0 // indirect

require (
	github.com/godbus/dbus/v5 v5.1.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
)
