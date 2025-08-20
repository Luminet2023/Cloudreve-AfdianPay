package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"cloudreve-afdianpay/internal/afdian"
	"cloudreve-afdianpay/internal/config"
	"cloudreve-afdianpay/internal/server"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
)

func main() {
	// 加载 .env
	_ = godotenv.Load(".env")

	// 初始化检查
	if err := config.ValidateEnv(); err != nil {
		log.Fatalf("%v", err)
	}
	fmt.Println("初始化检查通过")

	// Afdian 服务
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "./afdian_pay.db"
	}
	fmt.Printf("DB_PATH=%s\n", dbPath)
	svc := afdian.NewService(dbPath)
	if err := svc.EnsureDB(); err != nil {
		log.Fatalf("数据库初始化失败: %v", err)
	}

	// Gin
	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()

	s := server.NewServer(svc)
	r.POST("/afdian", s.AfdianCallback)
	r.POST("/order", s.Order)
	r.GET("/order", s.Order)

	port := os.Getenv("PORT")
	if port == "" {
		port = "9000"
	}

	fmt.Println("Cloudreve Afdian Pay Server\t已启动\nGithub: https://github.com/essesoul/Cloudreve-AfdianPay")
	fmt.Println("-------------------------")
	fmt.Println("程序运行端口：" + port)
	fmt.Printf("SITE_URL=%s\n", os.Getenv("SITE_URL"))

	if err := http.ListenAndServe(":"+port, r); err != nil {
		log.Fatalf("服务启动失败: %v", err)
	}
}
