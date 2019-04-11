package main

import (
	"fmt"
	"io/ioutil"
	"net/http"

	"github.com/gin-gonic/gin"
)

func main() {

	statusNode := NewStatusNode()
	statusNode.Start()

	r := gin.Default()
	r.LoadHTMLGlob("./cmd/statusnode/assets/tmpl/*")
	r.Static("static", "./cmd/statusnode/assets/static")

	r.GET("/", func(c *gin.Context) {
		if statusNode.State() == StateRestarting {
			c.HTML(http.StatusOK, "progress.tmpl", statusNode)
		} else {
			c.HTML(http.StatusOK, "index.tmpl", gin.H{
				"Enabled":     statusNode.MailserverRunning(),
				"StaticPeers": statusNode.StaticPeers(),
				"EnodeID":     statusNode.MailserverEnode(),
				"MaxPeers":    statusNode.MaxPeers(),
				"MyIP":        statusNode.MyIP(),
			})
		}
	})

	r.POST("/set", func(c *gin.Context) {
		bytes, _ := ioutil.ReadAll(c.Request.Body)
		fmt.Printf("BODY IS: %s\n", string(bytes))
		statusNode.AddOverride(string(bytes))
		go func() {
			statusNode.Stop()
			statusNode.Start()
		}()
		c.Redirect(http.StatusTemporaryRedirect, "/")
	})

	r.POST("/reset", func(c *gin.Context) {
		statusNode.ResetOverrides()
		go func() {
			statusNode.Stop()
			statusNode.Start()
		}()
		c.Redirect(http.StatusTemporaryRedirect, "/")
	})

	r.GET("/ping", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"message": "pong",
		})
	})
	r.Run() // listen and serve on 0.0.0.0:8080

	statusNode.Stop()
}
