package costmodel

import (
	"net/http"
	"testing"

	"github.com/julienschmidt/httprouter"
	"github.com/opencost/opencost/pkg/customcost"
)

func TestRegisterAccessesRoutesRegistersPrefixedAndLegacyRoutes(t *testing.T) {
	router := httprouter.New()
	a := &Accesses{}

	registerAccessesRoutes(router, a)

	testCases := []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/allocation/autocomplete"},
		{method: http.MethodGet, path: RoutePrefix + "/allocation/autocomplete"},
		{method: http.MethodGet, path: "/clusterInfo"},
		{method: http.MethodGet, path: RoutePrefix + "/clusterInfo"},
		{method: http.MethodGet, path: "/customPricing"},
		{method: http.MethodGet, path: RoutePrefix + "/customPricing"},
		{method: http.MethodPost, path: "/spotUpdate"},
		{method: http.MethodPost, path: RoutePrefix + "/spotUpdate"},
	}

	for _, tc := range testCases {
		handle, _, _ := router.Lookup(tc.method, tc.path)
		if handle == nil {
			t.Fatalf("expected route %s %s to be registered", tc.method, tc.path)
		}
	}
}

func TestInitializeCloudCostRegistersPrefixedAndLegacyRoutes(t *testing.T) {
	router := httprouter.New()

	InitializeCloudCost(router)

	testCases := []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/cloudCost"},
		{method: http.MethodGet, path: RoutePrefix + "/cloudCost"},
		{method: http.MethodGet, path: "/cloud/config/export"},
		{method: http.MethodGet, path: RoutePrefix + "/cloud/config/export"},
	}

	for _, tc := range testCases {
		handle, _, _ := router.Lookup(tc.method, tc.path)
		if handle == nil {
			t.Fatalf("expected route %s %s to be registered", tc.method, tc.path)
		}
	}
}

func TestInitializeCustomCostRegistersPrefixedAndLegacyRoutes(t *testing.T) {
	router := httprouter.New()
	hourlyRepo := customcost.NewMemoryRepository()
	dailyRepo := customcost.NewMemoryRepository()
	ingConfig := customcost.DefaultIngestorConfiguration()
	customCostQuerier := customcost.NewRepositoryQuerier(hourlyRepo, dailyRepo, ingConfig.HourlyDuration, ingConfig.DailyDuration)
	customCostQueryService := customcost.NewQueryService(customCostQuerier)

	registerCustomCostRoutes(router, customCostQueryService)

	testCases := []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/customCost/total"},
		{method: http.MethodGet, path: RoutePrefix + "/customCost/total"},
		{method: http.MethodGet, path: "/customCost/timeseries"},
		{method: http.MethodGet, path: RoutePrefix + "/customCost/timeseries"},
	}

	for _, tc := range testCases {
		handle, _, _ := router.Lookup(tc.method, tc.path)
		if handle == nil {
			t.Fatalf("expected route %s %s to be registered", tc.method, tc.path)
		}
	}
}
