#!/usr/bin/env bash

REF=v1.4.3

wget https://github.com/hashicorp/nomad/raw/${REF}/drivers/exec/driver.go -O driver.go
wget https://github.com/hashicorp/nomad/raw/${REF}/drivers/exec/handle.go -O handle.go
wget https://github.com/hashicorp/nomad/raw/${REF}/drivers/exec/state.go -O state.go
