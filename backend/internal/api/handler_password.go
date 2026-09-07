package api

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"zip-queue/internal/model"
)

type passwordRequest struct {
	Value     string `json:"value" binding:"required"`
	SortOrder *int   `json:"sort_order"`
	Note      string `json:"note"`
	Enabled   *bool  `json:"enabled"`
}

// ListPasswords 返回全部密码，按 sort_order asc、id asc 排序。
func (a *API) ListPasswords(c *gin.Context) {
	var items []model.Password
	if err := a.db.Order("sort_order asc, id asc").Find(&items).Error; err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"items": items, "total": len(items)})
}

// CreatePassword 新增一个密码。未指定 sort_order 时追加到末尾。
func (a *API) CreatePassword(c *gin.Context) {
	var req passwordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	pw := &model.Password{Value: req.Value, Note: req.Note, Enabled: true}
	if req.Enabled != nil {
		pw.Enabled = *req.Enabled
	}
	if req.SortOrder != nil {
		pw.SortOrder = *req.SortOrder
	} else {
		// 追加到末尾：取当前最大 sort_order + 1
		var maxOrder int
		if err := a.db.Model(&model.Password{}).Select("COALESCE(MAX(sort_order), 0)").Scan(&maxOrder).Error; err == nil {
			pw.SortOrder = maxOrder + 1
		}
	}
	if err := a.db.Create(pw).Error; err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(201, pw)
}

// UpdatePassword 更新密码内容/排序/备注。
func (a *API) UpdatePassword(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid id"})
		return
	}
	var req passwordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	updates := map[string]interface{}{
		"value": req.Value,
		"note":  req.Note,
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if req.SortOrder != nil {
		updates["sort_order"] = *req.SortOrder
	}
	res := a.db.Model(&model.Password{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		c.JSON(500, gin.H{"error": res.Error.Error()})
		return
	}
	if res.RowsAffected == 0 {
		c.JSON(404, gin.H{"error": "password not found"})
		return
	}
	var pw model.Password
	a.db.First(&pw, id)
	c.JSON(200, pw)
}

// DeletePassword 删除密码。
func (a *API) DeletePassword(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid id"})
		return
	}
	res := a.db.Delete(&model.Password{}, id)
	if res.Error != nil {
		c.JSON(500, gin.H{"error": res.Error.Error()})
		return
	}
	if res.RowsAffected == 0 {
		c.JSON(404, gin.H{"error": "password not found"})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

// SetPasswordEnabled 启用/停用某个密码。停用后该密码在解压轮询时会被跳过。
func (a *API) SetPasswordEnabled(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid id"})
		return
	}
	var req struct {
		Enabled *bool `json:"enabled" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	res := a.db.Model(&model.Password{}).Where("id = ?", id).Update("enabled", *req.Enabled)
	if res.Error != nil {
		c.JSON(500, gin.H{"error": res.Error.Error()})
		return
	}
	if res.RowsAffected == 0 {
		c.JSON(404, gin.H{"error": "password not found"})
		return
	}
	var pw model.Password
	a.db.First(&pw, id)
	c.JSON(200, pw)
}

// ReorderPasswords 批量更新密码排序，接收 [{id, sort_order}] 列表。
func (a *API) ReorderPasswords(c *gin.Context) {
	var req struct {
		Items []struct {
			ID        uint `json:"id" binding:"required"`
			SortOrder int  `json:"sort_order"`
		} `json:"items" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	tx := a.db.Begin()
	for _, item := range req.Items {
		if err := tx.Model(&model.Password{}).Where("id = ?", item.ID).Update("sort_order", item.SortOrder).Error; err != nil {
			tx.Rollback()
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
	}
	if err := tx.Commit().Error; err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}
