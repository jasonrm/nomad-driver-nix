package main

import (
	_ "embed"
	"encoding/json"

	"github.com/jasonrm/nomad-driver-nix/nix"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/nomad/plugins"
)

//go:embed metadata.json
var metadataBytes []byte

// version is set from metadata.json, overridable at build time via ldflags.
var version = func() string {
	var m struct{ Version string }
	if err := json.Unmarshal(metadataBytes, &m); err != nil {
		return "unknown"
	}
	return m.Version
}()

func main() {
	nix.PluginVersion = version
	plugins.Serve(factory)
}

func factory(log hclog.Logger) interface{} {
	return nix.NewPlugin(log)
}
