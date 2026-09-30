package conf

import (
	"fmt"
	"os"

	"github.com/spf13/viper"
)

const DefaultNodeRetryCount = 1
const DefaultNodeTimeout = 15

type Conf struct {
	LogConfig   LogConfig    `mapstructure:"Log"`
	NodeConfigs []NodeConfig `mapstructure:"Nodes"`
	PprofPort   int          `mapstructure:"PprofPort"`
}

type LogConfig struct {
	Level  string `mapstructure:"Level"`
	Output string `mapstructure:"Output"`
	Access string `mapstructure:"Access"`
}

type NodeConfig struct {
	APIHost    string `mapstructure:"ApiHost"`
	NodeID     int    `mapstructure:"NodeID"`
	Key        string `mapstructure:"ApiKey"`
	Timeout    int    `mapstructure:"Timeout"`
	RetryCount *int   `mapstructure:"RetryCount"`
	// Protocol is auto (default), v3ui, or v1.
	Protocol string `mapstructure:"Protocol"`
}

func New() *Conf {
	return &Conf{
		LogConfig: LogConfig{
			Level:  "info",
			Output: "",
			Access: "none",
		},
	}
}

func (p *Conf) LoadFromPath(filePath string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open config file error: %s", err)
	}
	defer f.Close()
	v := viper.New()
	v.SetConfigFile(filePath)
	if err := v.ReadInConfig(); err != nil {
		return fmt.Errorf("read config file error: %s", err)
	}
	if err := v.Unmarshal(p); err != nil {
		return fmt.Errorf("unmarshal config error: %s", err)
	}
	for i := range p.NodeConfigs {
		if p.NodeConfigs[i].RetryCount == nil {
			p.NodeConfigs[i].RetryCount = intPtr(DefaultNodeRetryCount)
		}
	}
	return p.Validate()
}

// Validate checks the fields every node needs to reach its panel.
func (p *Conf) Validate() error {
	if len(p.NodeConfigs) == 0 {
		return fmt.Errorf("config has no Nodes")
	}
	for i, n := range p.NodeConfigs {
		switch {
		case n.APIHost == "":
			return fmt.Errorf("node %d: ApiHost is empty", i)
		case n.NodeID <= 0:
			return fmt.Errorf("node %d: NodeID must be positive", i)
		case n.Key == "":
			return fmt.Errorf("node %d: ApiKey is empty", i)
		}
	}
	return nil
}

func intPtr(v int) *int {
	return &v
}
