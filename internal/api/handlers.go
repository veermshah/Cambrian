package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Each handler is one tiny function — keep them in this one file because
// they all share the (c *gin.Context) → store → wrapList shape, and
// fanning into fourteen 12-line files is more navigation friction than
// it's worth. The spec lists fourteen handlers; we expose all fifteen
// routes (agent detail is its own handler off the same agents file).

func (s *Server) listAgents(c *gin.Context) {
	opts := ListAgentsOpts{
		Chain:     c.Query("chain"),
		NodeClass: c.Query("node_class"),
		Status:    c.Query("status"),
		TaskType:  c.Query("task_type"),
	}
	rows, err := s.cfg.Store.ListAgents(c.Request.Context(), opts)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, wrapList(rows))
}

func (s *Server) getAgent(c *gin.Context) {
	id := c.Param("id")
	d, err := s.cfg.Store.GetAgent(c.Request.Context(), id)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, d)
}

func (s *Server) listTrades(c *gin.Context) {
	opts := ListTradesOpts{
		AgentID: c.Query("agent_id"),
		Chain:   c.Query("chain"),
		Limit:   queryInt(c, "limit", 1000),
	}
	rows, err := s.cfg.Store.ListTrades(c.Request.Context(), opts)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, wrapList(rows))
}

func (s *Server) listEpochs(c *gin.Context) {
	limit := queryInt(c, "limit", 500)
	rows, err := s.cfg.Store.ListEpochs(c.Request.Context(), limit)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, wrapList(rows))
}

func (s *Server) getLineage(c *gin.Context) {
	rows, err := s.cfg.Store.GetLineage(c.Request.Context())
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, wrapList(rows))
}

func (s *Server) getTreasury(c *gin.Context) {
	state, err := s.cfg.Store.GetTreasury(c.Request.Context())
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, state)
}

func (s *Server) listPostmortems(c *gin.Context) {
	limit := queryInt(c, "limit", 500)
	rows, err := s.cfg.Store.ListPostmortems(c.Request.Context(), limit)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, wrapList(rows))
}

func (s *Server) listOffspring(c *gin.Context) {
	rows, err := s.cfg.Store.ListOffspring(c.Request.Context(), c.Query("status"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, wrapList(rows))
}

func (s *Server) getBudget(c *gin.Context) {
	state, err := s.cfg.Store.GetBudget(c.Request.Context())
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, state)
}

func (s *Server) getCircuitBreaker(c *gin.Context) {
	state, err := s.cfg.Store.GetCircuitBreaker(c.Request.Context())
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, state)
}

func (s *Server) listBacktests(c *gin.Context) {
	limit := queryInt(c, "limit", 200)
	rows, err := s.cfg.Store.ListBacktests(c.Request.Context(), limit)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, wrapList(rows))
}

func (s *Server) listIntel(c *gin.Context) {
	opts := ListIntelOpts{
		Channel:   c.Query("channel"),
		Sentiment: c.Query("sentiment"),
		Limit:     queryInt(c, "limit", 1000),
	}
	rows, err := s.cfg.Store.ListIntel(c.Request.Context(), opts)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, wrapList(rows))
}

func (s *Server) listModels(c *gin.Context) {
	rows, err := s.cfg.Store.ListModels(c.Request.Context())
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, wrapList(rows))
}

func (s *Server) listEvolution(c *gin.Context) {
	limit := queryInt(c, "limit", 500)
	rows, err := s.cfg.Store.ListEvolution(c.Request.Context(), limit)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, wrapList(rows))
}

func (s *Server) getDashboardSnapshot(c *gin.Context) {
	snap, err := s.cfg.Store.GetDashboardSnapshot(c.Request.Context())
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, snap)
}
