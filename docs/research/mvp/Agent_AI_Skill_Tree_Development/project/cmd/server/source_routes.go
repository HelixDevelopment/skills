// cmd/server — source_routes.go defines the REST endpoints for skill source
// management (G84). These routes let operators register, list, inspect,
// delete, and trigger sync for skill sources (GitHub repos, filesystem paths,
// URLs) that supply SKILL.md files for the source-ingestion pipeline.
//
// The handlers are extracted into RegisterSourceRoutes so buildRouter stays
// focused on top-level wiring.
package main

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/helixdevelopment/skill-system/internal/skillsource"
)

// RegisterSourceRoutes mounts the /api/v1/sources endpoints on the provided
// router group. store provides CRUD access to the skill_sources table; orch
// drives the fetch -> parse -> map -> dedup -> import sync pipeline.
func RegisterSourceRoutes(rg *gin.RouterGroup, store *skillsource.Store, orch *skillsource.Orchestrator) {
	sources := rg.Group("/sources")
	{
		// POST /sources — register a new source
		sources.POST("", createSourceHandler(store))

		// GET /sources — list all sources
		sources.GET("", listSourcesHandler(store))

		// GET /sources/:id — get source by ID
		sources.GET("/:id", getSourceHandler(store))

		// DELETE /sources/:id — delete source
		sources.DELETE("/:id", deleteSourceHandler(store))

		// POST /sources/:id/sync — trigger sync via orchestrator
		sources.POST("/:id/sync", syncSourceHandler(store, orch))
	}
}

// createSourceHandler handles POST /api/v1/sources — register a new source.
// Validates the request body, calls store.Create, and returns the created
// source with 201. Returns 409 on name conflict, 400 on validation failure.
func createSourceHandler(store *skillsource.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		var src skillsource.SkillSource
		if err := c.ShouldBindJSON(&src); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := store.Create(c.Request.Context(), &src); err != nil {
			if errors.Is(err, skillsource.ErrSourceExists) {
				c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
				return
			}
			if errors.Is(err, skillsource.ErrInvalidSource) {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusCreated, src)
	}
}

// listSourcesHandler handles GET /api/v1/sources — list all sources.
// Accepts an optional "enabled" query parameter to filter to only enabled
// sources. Returns 200 with the array of sources and a count.
func listSourcesHandler(store *skillsource.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		enabledOnly := c.Query("enabled") == "true"
		srcs, err := store.List(c.Request.Context(), enabledOnly)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"sources": srcs, "count": len(srcs)})
	}
}

// getSourceHandler handles GET /api/v1/sources/:id — get source by ID.
// Returns 200 with the source, or 404 if not found.
func getSourceHandler(store *skillsource.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid source id"})
			return
		}
		src, err := store.GetByID(c.Request.Context(), id)
		if err != nil {
			if errors.Is(err, skillsource.ErrSourceNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, src)
	}
}

// deleteSourceHandler handles DELETE /api/v1/sources/:id — delete source.
// Returns 204 on success, 404 if not found.
func deleteSourceHandler(store *skillsource.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid source id"})
			return
		}
		if err := store.Delete(c.Request.Context(), id); err != nil {
			if errors.Is(err, skillsource.ErrSourceNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusNoContent, nil)
	}
}

// syncSourceHandler handles POST /api/v1/sources/:id/sync — trigger a full
// sync for the source via the orchestrator. The orchestrator runs the
// fetch -> parse -> map -> dedup -> import pipeline and manages sync-status
// transitions internally. Returns 200 with the SyncResult, or 404/400 on
// input errors.
func syncSourceHandler(store *skillsource.Store, orch *skillsource.Orchestrator) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid source id"})
			return
		}
		// Verify the source exists before triggering a sync.
		if _, err := store.GetByID(c.Request.Context(), id); err != nil {
			if errors.Is(err, skillsource.ErrSourceNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		result, err := orch.SyncSource(c.Request.Context(), id)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, result)
	}
}
