package config

import (
	"errors"
	"log"
	"os"
)

// ValidateEnv 检查必须的环境变量
func ValidateEnv() error {
	if _, err := os.Stat(".env"); err != nil {
		return errors.New("未找到.env文件,已停止运行")
	}
	log.Printf("[Config] validating env variables")
	if os.Getenv("SITE_URL") == "" {
		return errors.New("SITE_URL未设置,已停止运行")
	}
	if os.Getenv("COMMUNICATION_KEY") == "" {
		return errors.New("COMMUNICATION_KEY未设置,已停止运行")
	}
	if os.Getenv("USER_ID") == "" {
		return errors.New("USER_ID未设置,已停止运行")
	}
	if os.Getenv("TOKEN") == "" {
		return errors.New("TOKEN未设置,已停止运行")
	}
	if os.Getenv("PORT") == "" {
		return errors.New("PORT未设置,已停止运行")
	}
	return nil
}
