package main

import (
	"github.com/spf13/viper"
	"gopkg.in/yaml.v2"
)

func loadConfig(configFile string) ([]byte, *hcsConfig, error) {
	viper.SetConfigName(configFile)
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	viper.AddConfigPath("/")

	var err error
	if err = viper.ReadInConfig(); err != nil {
		return nil, nil, err
	}

	var fabricConfig []byte
	if viper.IsSet("fabric") {
		if fabricConfig, err = yaml.Marshal(viper.Get("fabric")); err != nil {
			return nil, nil, err
		}
	}

	var hcs hcsConfig
	if err = viper.UnmarshalKey("hcs", &hcs); err != nil {
		return nil, nil, err
	}

	return fabricConfig, &hcs, nil
}
