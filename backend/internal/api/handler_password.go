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

// passwordExists 判断 value 是否已被占用；excludeID 非 0 时排除该记录（用于更新自身）。
// 密码按字面量尝试解压，故区分大小写；停用密码同样参与校验（重复条目没有意义）。
func (a *API) passwordExists(value string, excludeID uint) (bool, error) {
	q := a.db.Model(&model.Password{}).Where("value = ?", value)
	if excludeID != 0 {
		q = q.Where("id != ?", excludeID)
	}
	var count int64
	if err := q.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// ListPasswords 返回密码列表（分页 + 排序）。默认按 sort_order asc、id asc 排序。
// 查询参数：page（默认 1）、page_size（默认 20，上限 200）、sort_by、desc。
func (a *API) ListPasswords(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 20
	}

	// 排序字段白名单，避免 SQL 注入
	order := "sort_order asc, id asc"
	if col, ok := map[string]string{
		"value":      "value",
		"note":       "note",
		"enabled":    "enabled",
		"sort_order": "sort_order",
	}[c.Query("sort_by")]; ok {
		dir := "asc"
		if c.Query("desc") == "true" {
			dir = "desc"
		}
		order = col + " " + dir
	}

	var total int64
	if err := a.db.Model(&model.Password{}).Count(&total).Error; err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	var items []model.Password
	if err := a.db.Order(order).Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error; err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{
		"items":     items,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// CreatePassword 新增一个密码。未指定 sort_order 时追加到末尾。
func (a *API) CreatePassword(c *gin.Context) {
	var req passwordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	// 重复校验：同一密码只允许存在一条
	exists, err := a.passwordExists(req.Value, 0)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if exists {
		c.JSON(400, gin.H{"error": "该密码已存在，请勿重复添加"})
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
	// 重复校验：排除自身，避免仅修改备注时误判为重复
	exists, err := a.passwordExists(req.Value, uint(id))
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if exists {
		c.JSON(400, gin.H{"error": "该密码已存在，请勿重复添加"})
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

// ReorderPasswords 将某密码与拖放目标密码交换位置（一次调用交换两条记录）。
// 请求体 {id, target_id}：把 id 对应记录与 target_id 对应记录的 sort_order 互换。
// 仅更新这两条记录，不涉及其余条目，也不重排全局顺序。
func (a *API) ReorderPasswords(c *gin.Context) {
	var req struct {
		ID       uint `json:"id" binding:"required"`
		TargetID uint `json:"target_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if req.ID == req.TargetID {
		c.JSON(200, gin.H{"ok": true})
		return
	}

	// 取出两条记录
	var src, dst model.Password
	if err := a.db.First(&src, req.ID).Error; err != nil {
		c.JSON(404, gin.H{"error": "password not found"})
		return
	}
	if err := a.db.First(&dst, req.TargetID).Error; err != nil {
		c.JSON(404, gin.H{"error": "password not found"})
		return
	}

	// 互换两条记录的 sort_order（单事务、两条 UPDATE）
	tx := a.db.Begin()
	if err := tx.Model(&model.Password{}).Where("id = ?", src.ID).
		Update("sort_order", dst.SortOrder).Error; err != nil {
		tx.Rollback()
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if err := tx.Model(&model.Password{}).Where("id = ?", dst.ID).
		Update("sort_order", src.SortOrder).Error; err != nil {
		tx.Rollback()
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if err := tx.Commit().Error; err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}
