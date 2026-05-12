// @title           OpenCost API
// @version         0.1
// @description     The OpenCost API provides real-time and historical reporting of Kubernetes cloud costs.
// @license.name    Apache 2.0
// @license.url     https://www.apache.org/licenses/LICENSE-2.0.html
// @host            localhost:9003
// @BasePath        /
package main

import (
	"github.com/opencost/opencost/pkg/cmd"
	"github.com/rs/zerolog/log"
)

func main() {
	// runs the appropriate application mode using the default cost-model command
	// see: github.com/opencost/opencost/pkg/cmd package for details
	if err := cmd.Execute(nil); err != nil {
		log.Fatal().Err(err)
	}
}
