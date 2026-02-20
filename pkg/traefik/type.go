package traefik

import (
	"encoding/json"
	"io"

	"github.com/goccy/go-yaml"
)

type Config struct {
	HTTP HTTPConfig `json:"http"`
}

func (c *Config) ToJSON(w io.Writer) error {
	var inf any
	if len(c.HTTP.Routers) == 0 && len(c.HTTP.Services) == 0 {
		inf = map[string]any{}
	} else {
		inf = c
	}
	return json.NewEncoder(w).Encode(inf)
}

func (c *Config) ToYAML(w io.Writer) error {
	var inf any
	if len(c.HTTP.Routers) == 0 && len(c.HTTP.Services) == 0 {
		inf = map[string]any{}
	} else {
		inf = c
	}
	return yaml.NewEncoder(w).Encode(inf)
}

type HTTPConfig struct {
	Routers     map[string]RouterConfig   `json:"routers,omitempty"`
	Services    map[string]ServiceConfig  `json:"services,omitempty"`
	Middlewares map[string]map[string]any `json:"middlewares,omitempty"`
}

type RouterConfig struct {
	Rule        string   `json:"rule"`
	Service     string   `json:"service"`
	Middlewares []string `json:"middlewares,omitempty"`
}

type ServiceConfig struct {
	LoadBalancer LoadBalancerConfig
}

type LoadBalancerConfig struct {
	Servers []ServerConfig `json:"servers"`
}

type ServerConfig struct {
	URL string `json:"url"`
}
