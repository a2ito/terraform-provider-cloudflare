package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/a2-ito/terraform-provider-cloudflare/internal/provider"
)

// version はリリース時に ldflags で上書きする。
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "delve などのデバッガから起動する場合に指定する")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/a2-ito/cloudflare",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
