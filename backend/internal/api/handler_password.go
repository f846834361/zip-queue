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

// ReorderPasswords 将某密码与相邻密码交换位置（上移/下移一位）。
// 请求体仅 {id, direction}：direction = -1 上移一位，1 下移一位，恒定小报文。
// 相邻交换只改写两条记录的 sort_order，其余记录不动。
func (a *API) ReorderPasswords(c *gin.Context) {
	var req struct {
		ID        uint `json:"id" binding:"required"`
		Direction int  `json:"direction"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if req.Direction != -1 && req.Direction != 1 {
		c.JSON(400, gin.H{"error": "direction must be -1 (up) or 1 (down)"})
		return
	}

	// 当前完整顺序
	var list []model.Password
	if err := a.db.Order("sort_order asc, id asc").Find(&list).Error; err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	cur := -1
	for i := range list {
		if list[i].ID == req.ID {
			cur = i
			break
		}
	}
	if cur == -1 {
		c.JSON(404, gin.H{"error": "password not found"})
		return
	}
	adj := cur + req.Direction
	if adj < 0 || adj >= len(list) {
		c.JSON(200, gin.H{"ok": true}) // 已在边界，无需处理
		return
	}

	// 若存在重复 sort_order（历史脏数据），先整表归一化为 0..n-1，保证交换有意义。
	duplicated := false
	seen := make(map[int]struct{}, len(list))
	for i := range list {
		if _, ok := seen[list[i].SortOrder]; ok {
			duplicated = true
			break
		}
		seen[list[i].SortOrder] = struct{}{}
	}

	tx := a.db.Begin()
	if duplicated {
		for i := range list {
			if list[i].SortOrder == i {
				continue
			}
			if err := tx.Model(&model.Password{}).Where("id = ?", list[i].ID).Update("sort_order", i).Error; err != nil {
				tx.Rollback()
				c.JSON(500, gin.H{"error": err.Error()})
				return
			}
			list[i].SortOrder = i
		}
	}

	// 交换相邻两条的 sort_order
	a_, b_ := list[cur], list[adj]
	if err := tx.Model(&model.Password{}).Where("id = ?", a_.ID).Update("sort_order", b_.SortOrder).Error; err != nil {
		tx.Rollback()
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if err := tx.Model(&model.Password{}).Where("id = ?", b_.ID).Update("sort_order", a_.SortOrder).Error; err != nil {
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
