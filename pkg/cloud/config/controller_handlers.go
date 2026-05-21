package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/julienschmidt/httprouter"
	proto "github.com/opencost/opencost/core/pkg/protocol"
	"github.com/opencost/opencost/pkg/cloud"
	"github.com/opencost/opencost/pkg/cloud/aws"
	"github.com/opencost/opencost/pkg/cloud/azure"
	"github.com/opencost/opencost/pkg/cloud/gcp"
)

var protocol = proto.HTTP()

func (c *Controller) cloudCostChecks() func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	// If Pipeline is nil, always return 503
	if c == nil {
		return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
			http.Error(w, "ConfigController: is nil", http.StatusServiceUnavailable)
		}
	}

	return nil
}

// GetExportConfigHandler creates a handler from a http request which exports an integration via the integrationController
// GetExportConfigHandler exports cloud integration configs with secrets sanitized.
// @Summary      导出云集成配置
// @Tags         CloudConfig
// @Description  导出全部或指定 integrationKey 的云集成配置，返回内容会自动脱敏。通常受管理员鉴权保护。
// @Param        integrationKey  query  string  false  "指定要导出的集成 Key。"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {string}  string
// @Failure      503  {string}  string
// @Router       /cloud/config/export [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/cloud/config/export [get]
func (c *Controller) GetExportConfigHandler() func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	// perform basic checks to ensure that the pipeline can be accessed
	fn := c.cloudCostChecks()
	if fn != nil {
		return fn
	}

	// Return valid handler func
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		w.Header().Set("Content-Type", "application/json")

		integrationKey := r.URL.Query().Get("integrationKey")

		configs, err := c.ExportConfigs(integrationKey)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		protocol.WriteDataWithMessage(w, configs, "Configurations have been sanitized to protect secrets")
	}
}

// GetAddConfigHandler creates a handler from a http request which adds an integration via the integrationController
func (c *Controller) GetAddConfigHandler() func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	// perform basic checks to ensure that the pipeline can be accessed
	fn := c.cloudCostChecks()
	if fn != nil {
		return fn
	}

	// Return valid handler func
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		w.Header().Set("Content-Type", "application/json")

		configType := r.URL.Query().Get("type")
		if configType == "" {
			http.Error(w, "'type' parameter is required", http.StatusBadRequest)
			return
		}

		config, err := ParseConfig(configType, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		err = c.CreateConfig(config)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		protocol.WriteData(w, fmt.Sprintf("Successfully added integration with key %s", config.Key()))
	}
}

// GetUpdateConfigHandler creates a handler from a http request which updates an existing integration via the integrationController
func (c *Controller) GetUpdateConfigHandler() func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	// perform basic checks to ensure that the pipeline can be accessed
	fn := c.cloudCostChecks()
	if fn != nil {
		return fn
	}

	// Return valid handler func
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		w.Header().Set("Content-Type", "application/json")

		configType := r.URL.Query().Get("type")
		if configType == "" {
			http.Error(w, "'type' parameter is required", http.StatusBadRequest)
			return
		}

		config, err := ParseConfig(configType, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		err = c.UpdateConfig(config)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		protocol.WriteData(w, fmt.Sprintf("Successfully updated integration with key %s", config.Key()))
	}
}

func ParseConfig(configType string, body io.Reader) (cloud.KeyedConfig, error) {
	buf := new(bytes.Buffer)
	_, err := buf.ReadFrom(body)
	if err != nil {
		return nil, fmt.Errorf("failed to read body: %w", err)
	}
	bytes := buf.Bytes()
	switch strings.ToLower(configType) {
	case S3ConfigType:
		config := &aws.S3Configuration{}
		err = json.Unmarshal(bytes, config)
		if err != nil {
			return nil, fmt.Errorf("error unmarshalling S3 Configuration: %w", err)
		}
		return config, nil
	case AthenaConfigType:
		config := &aws.AthenaConfiguration{}
		err = json.Unmarshal(bytes, config)
		if err != nil {
			return nil, fmt.Errorf("error unmarshalling Athena Configuration: %w", err)
		}
		return config, nil
	case BigQueryConfigType:
		config := &gcp.BigQueryConfiguration{}
		err = json.Unmarshal(bytes, config)
		if err != nil {
			return nil, fmt.Errorf("error unmarshalling Big Query Configuration: %w", err)
		}
		return config, nil
	case AzureStorageConfigType:
		config := &azure.StorageConfiguration{}
		err = json.Unmarshal(bytes, config)
		if err != nil {
			return nil, fmt.Errorf("error unmarshalling Azure Storage Configuration: %w", err)
		}
		return config, nil

	}
	return nil, fmt.Errorf("provided config type was not recognised %s", configType)
}

// GetEnableConfigHandler creates a handler from a http request which enables an integration via the integrationController
// GetEnableConfigHandler enables a cloud integration config.
// @Summary      启用云集成配置
// @Tags         CloudConfig
// @Description  启用指定 integrationKey 和 source 的云集成配置。通常受管理员鉴权保护。
// @Param        integrationKey  query  string  true  "集成 Key。"
// @Param        source          query  string  true  "配置来源。"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {string}  string
// @Failure      503  {string}  string
// @Router       /cloud/config/enable [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/cloud/config/enable [get]
func (c *Controller) GetEnableConfigHandler() func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	// perform basic checks to ensure that the pipeline can be accessed
	fn := c.cloudCostChecks()
	if fn != nil {
		return fn
	}

	// Return valid handler func
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		w.Header().Set("Content-Type", "application/json")

		integrationKey := r.URL.Query().Get("integrationKey")
		if integrationKey == "" {
			http.Error(w, "required parameter 'integrationKey' is missing", http.StatusBadRequest)
			return
		}

		source := r.URL.Query().Get("source")
		if source == "" {
			http.Error(w, "required parameter 'source' is missing", http.StatusBadRequest)
			return
		}

		err := c.EnableConfig(integrationKey, source)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		protocol.WriteData(w, fmt.Sprintf("Successfully enabled integration with key %s from source %s", integrationKey, source))
	}
}

// GetDisableConfigHandler creates a handler from a http request which disables an integration via the integrationController
// GetDisableConfigHandler disables a cloud integration config.
// @Summary      停用云集成配置
// @Tags         CloudConfig
// @Description  停用指定 integrationKey 和 source 的云集成配置。通常受管理员鉴权保护。
// @Param        integrationKey  query  string  true  "集成 Key。"
// @Param        source          query  string  true  "配置来源。"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {string}  string
// @Failure      503  {string}  string
// @Router       /cloud/config/disable [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/cloud/config/disable [get]
func (c *Controller) GetDisableConfigHandler() func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	// perform basic checks to ensure that the pipeline can be accessed
	fn := c.cloudCostChecks()
	if fn != nil {
		return fn
	}

	// Return valid handler func
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		w.Header().Set("Content-Type", "application/json")

		integrationKey := r.URL.Query().Get("integrationKey")
		if integrationKey == "" {
			http.Error(w, "required parameter 'integrationKey' is missing", http.StatusBadRequest)
			return
		}

		source := r.URL.Query().Get("source")
		if source == "" {
			http.Error(w, "required parameter 'source' is missing", http.StatusBadRequest)
			return
		}

		err := c.DisableConfig(integrationKey, source)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		protocol.WriteData(w, fmt.Sprintf("Successfully disabled integration with key %s from source %s", integrationKey, source))
	}
}

// GetDeleteConfigHandler creates a handler from a http request which deletes an integration via the integrationController
// if there are no other integrations with the given integration key, it also clears the data.
// GetDeleteConfigHandler deletes a cloud integration config.
// @Summary      删除云集成配置
// @Tags         CloudConfig
// @Description  删除指定 integrationKey 的云集成配置。通常受管理员鉴权保护。
// @Param        integrationKey  query  string  true  "集成 Key。"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {string}  string
// @Failure      503  {string}  string
// @Router       /cloud/config/delete [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/cloud/config/delete [get]
func (c *Controller) GetDeleteConfigHandler() func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	// perform basic checks to ensure that the pipeline can be accessed
	fn := c.cloudCostChecks()
	if fn != nil {
		return fn
	}

	// Return valid handler func
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		w.Header().Set("Content-Type", "application/json")

		integrationKey := r.URL.Query().Get("integrationKey")
		if integrationKey == "" {
			http.Error(w, "required parameter 'integrationKey' is missing", http.StatusBadRequest)
			return
		}

		err := c.DeleteConfig(integrationKey, ConfigControllerSource.String())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		protocol.WriteData(w, fmt.Sprintf("Successfully deleted integration with key %s", integrationKey))
	}
}
