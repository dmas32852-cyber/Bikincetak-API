package main

import (
	"bikincetak-api/config"
	"bikincetak-api/database"
	"bikincetak-api/routes"
	"fmt"
	"log"
	"os"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/joho/godotenv"
)

func main() {
	err := godotenv.Load()
	if err != nil {
		fmt.Println("Env gak kebaca")
	}

	app := fiber.New()

	frontend_url := os.Getenv("FRONTEND_URL")
	if frontend_url == "" {
		frontend_url ="https://bikin-cetak.vercel.app"
	}
	app.Use(cors.New(cors.Config{
		AllowOrigins:     frontend_url,
		AllowHeaders:     "Origin, Content-Type, Accept, Authorization",
		AllowMethods:     "GET, POST, HEAD, PUT, DELETE, PATCH",
		AllowCredentials: true,
	}))

	database.ConnectDB()
	database.ConnectRedis()
	config.ConnectMidtrans()
	routes.SetupRoutes(app)

	fmt.Println("Server sedang berjalan di Port: 3000")
	log.Fatal(app.Listen(":3000"))
}
