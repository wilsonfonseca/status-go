package main

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func main() {
	r := gin.Default()
	r.LoadHTMLGlob("./cmd/statusnode/assets/tmpl/*")
	r.Static("static", "./cmd/statusnode/assets/static")

	r.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "index.tmpl", gin.H{
			"Enabled": true,
			"StaticPeers": []string{
				"one", "two", "tree",
			},
			"EnodeID":  "enode://ffffffffffffffff:pAss@127.0.0.1:99999",
			"MaxPeers": 30,
		})
	})

	r.GET("/ping", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"message": "pong",
		})
	})
	r.Run() // listen and serve on 0.0.0.0:8080
}
