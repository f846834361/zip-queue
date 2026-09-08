package api

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm/clause"

	"zip-queue/internal/model"
)

// settingKeyPenetrateSubfolders 批量操作"穿透文件夹"开关的存储 key。
const settingKeyPenetrateSubfolders = "penetrate_subfolders"

// getSetting 读取指定 key 的配置值，不存在返回空串。
func (a *API) getSetting(key string) string {
	var s model.Setting
	if err := a.db.Where("key = ?", key).First(&s).Error; err != nil {
		return ""
	}
	return s.Value
}

// setSetting 写入配置，key 已存在时覆盖。
func (a *API) setSetting(key, value string) error {
	s := model.Setting{Key: key, Value: value, UpdatedAt: time.Now()}
	return a.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).Create(&s).Error
}

// penetrateSubfolders 读取"穿透文件夹"开关，默认 false。
func (a *API) penetrateSubfolders() bool {
	v, err := strconv.ParseBool(a.getSetting(settingKeyPenetrateSubfolders))
	return err == nil && v
}

type updateConfigRequest struct {
	PenetrateSubfolders *bool `json:"penetrate_subfolders"`
}

// UpdateConfig 更新可在配置页修改的运行期配置项。
func (a *API) UpdateConfig(c *gin.Context) {
	var req updateConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if req.PenetrateSubfolders != nil {
		if err := a.setSetting(settingKeyPenetrateSubfolders, strconv.FormatBool(*req.PenetrateSubfolders)); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
	}
	c.JSON(200, a.configResponse())
}
