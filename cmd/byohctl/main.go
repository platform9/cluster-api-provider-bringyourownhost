// cmd/byohctl/main.go
package main

import (
    "os"
    "github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/cmd"
    "github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/utils"
)

func main() {
    if err := cmd.Execute(); err != nil {
        utils.LogError(err.Error())
        os.Exit(1)
    }
}