package main

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var studioVersion string

// Set by the Windows release builder from the actual compiled source inputs.
var buildID = "development"

type studioBuild struct {
	Version string `json:"version"`
	ID      string `json:"id"`
}

func buildInfo() studioBuild {
	return studioBuild{Version: strings.TrimSpace(studioVersion), ID: buildID}
}
