package server

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/cmfrunner"
	"github.com/ollama/ollama/version"
)

// CMFStatusHandler reports the Cortiq runtime the server will use for CMF
// models.
func (s *Server) CMFStatusHandler(c *gin.Context) {
	status := cmfrunner.Status(c.Request.Context())
	c.JSON(http.StatusOK, api.CMFStatusResponse{
		Version: version.Version,
		Runtime: api.CMFRuntimeStatus{
			Installed:         status.Installed,
			Path:              status.Path,
			Source:            string(status.Source),
			Version:           status.Version,
			PinnedVersion:     status.PinnedVersion,
			Platform:          status.Platform,
			PlatformSupported: status.PlatformSupported,
			InstallDir:        status.InstallDir,
			AutoInstall:       status.AutoInstall,
			Error:             status.Error,
		},
	})
}

// CMFRuntimeInstallHandler downloads and installs the pinned Cortiq runtime,
// streaming progress like a model pull.
func (s *Server) CMFRuntimeInstallHandler(c *gin.Context) {
	var req api.CMFRuntimeInstallRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ch := make(chan any)
	go func() {
		defer close(ch)
		ctx, cancel := context.WithCancel(c.Request.Context())
		defer cancel()
		if _, err := cmfrunner.Install(ctx, func(p api.ProgressResponse) { ch <- p }); err != nil {
			ch <- gin.H{"error": err.Error()}
		}
	}()

	if req.Stream != nil && !*req.Stream {
		waitForStream(c, ch)
		return
	}
	streamResponse(c, ch)
}
