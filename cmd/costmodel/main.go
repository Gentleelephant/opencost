package main

import (
	"github.com/opencost/opencost/pkg/cmd"
	"github.com/rs/zerolog/log"
)

// @title        OpenCost API
// @version      1.0
// @description  Swagger documentation for the OpenCost costmodel HTTP API.
// @license.name Apache 2.0
// @license.url  https://www.apache.org/licenses/LICENSE-2.0.html
// @schemes      http https

func main() {
	// runs the appropriate application mode using the default cost-model command
	// see: github.com/opencost/opencost/pkg/cmd package for details
	if err := cmd.Execute(nil); err != nil {
		log.Fatal().Err(err)
	}
}
