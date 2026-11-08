package config

import (
	"fmt"
	"mars_chat_server/internal/config"
	"testing"
)

func TestInit(t *testing.T) {
	conf := config.GetConfig()
	fmt.Println(conf.MainConfig)
	fmt.Println(conf.MysqlConfig)
	fmt.Println(conf.RedisConfig)
	fmt.Println(conf.AuthCodeConfig)
}
