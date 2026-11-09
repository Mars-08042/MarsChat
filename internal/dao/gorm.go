package dao

import (
	"fmt"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"mars_chat_server/internal/config"
	"mars_chat_server/internal/model"
	"mars_chat_server/pkg/zlog"
)

var GormDB *gorm.DB

func init() {
	conf := config.GetConfig()
	mysqlConfig := conf.MysqlConfig
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		mysqlConfig.User, mysqlConfig.Password, mysqlConfig.Host, mysqlConfig.Port, mysqlConfig.DatabaseName)
	var err error
	GormDB, err = gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		zlog.Fatal(err.Error())
	}
	err = GormDB.AutoMigrate(&model.UserInfo{}, &model.GroupInfo{}, &model.UserContact{}, &model.Session{}, &model.ContactApply{}, &model.Message{}) // 自动迁移，如果没有建表，会自动创建对应的表
	if err != nil {
		zlog.Fatal(err.Error())
	}
}
