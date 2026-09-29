package model

import (
	"context"
	"errors"
	"math/rand"
	"sort"
	"strings"

	"gorm.io/gorm"

	"github.com/infinmalum/one-gateway/common"
	"github.com/infinmalum/one-gateway/common/utils"
)

// GetRandomSatisfiedChannelByType selects a channel that can receive a native
// protocol request. A mixed-provider group must not randomly choose a channel
// that requires an unimplemented cross-protocol conversion.
func GetRandomSatisfiedChannelByType(group, model string, channelType int, ignoreFirstPriority bool) (*Channel, error) {
	return GetRandomSatisfiedChannelByTypeExcluding(group, model, channelType, ignoreFirstPriority, nil)
}

// GetRandomSatisfiedChannelExcluding serves protocol-neutral retries for
// legacy routes, whose provider type is selected from all compatible channels.
func GetRandomSatisfiedChannelExcluding(group, model string, ignoreFirstPriority bool, excluded map[int]bool) (*Channel, error) {
	return GetRandomSatisfiedChannelByTypeExcluding(group, model, -1, ignoreFirstPriority, excluded)
}

// GetRandomSatisfiedChannelByTypeExcluding chooses a compatible channel that
// has not already failed during the current native request.
func GetRandomSatisfiedChannelByTypeExcluding(group, model string, channelType int, ignoreFirstPriority bool, excluded map[int]bool) (*Channel, error) {
	var abilities []Ability
	groupColumn := "`group`"
	if common.UsingPostgreSQL {
		groupColumn = `"group"`
	}
	if err := DB.Where("enabled = ? AND model = ?", true, model).Where(groupColumn+" = ?", group).Find(&abilities).Error; err != nil {
		return nil, err
	}
	if len(abilities) == 0 {
		return nil, errors.New("channel not found")
	}
	ids := make([]int, 0, len(abilities))
	for _, ability := range abilities {
		ids = append(ids, ability.ChannelId)
	}
	var channels []Channel
	query := DB.Where("id IN ? AND status = ?", ids, ChannelStatusEnabled)
	if channelType >= 0 {
		query = query.Where("type = ?", channelType)
	}
	if err := query.Find(&channels).Error; err != nil {
		return nil, err
	}
	available := channels[:0]
	for _, channel := range channels {
		if !excluded[channel.Id] {
			available = append(available, channel)
		}
	}
	if len(available) == 0 {
		return nil, errors.New("channel not found")
	}
	sort.Slice(available, func(i, j int) bool { return available[i].GetPriority() > available[j].GetPriority() })
	firstPriority := available[0].GetPriority()
	end := len(available)
	if firstPriority > 0 {
		for i := range available {
			if available[i].GetPriority() != firstPriority {
				end = i
				break
			}
		}
	}
	if ignoreFirstPriority && end < len(available) {
		return &available[end+rand.Intn(len(available)-end)], nil
	}
	return &available[rand.Intn(end)], nil
}

type Ability struct {
	Group     string `json:"group" gorm:"type:varchar(32);primaryKey;autoIncrement:false"`
	Model     string `json:"model" gorm:"primaryKey;autoIncrement:false"`
	ChannelId int    `json:"channel_id" gorm:"primaryKey;autoIncrement:false;index"`
	Enabled   bool   `json:"enabled"`
	Priority  *int64 `json:"priority" gorm:"bigint;default:0;index"`
}

func GetRandomSatisfiedChannel(group string, model string, ignoreFirstPriority bool) (*Channel, error) {
	ability := Ability{}
	groupCol := "`group`"
	trueVal := "1"
	if common.UsingPostgreSQL {
		groupCol = `"group"`
		trueVal = "true"
	}

	var err error = nil
	var channelQuery *gorm.DB
	if ignoreFirstPriority {
		channelQuery = DB.Where(groupCol+" = ? and model = ? and enabled = "+trueVal, group, model)
	} else {
		maxPrioritySubQuery := DB.Model(&Ability{}).Select("MAX(priority)").Where(groupCol+" = ? and model = ? and enabled = "+trueVal, group, model)
		channelQuery = DB.Where(groupCol+" = ? and model = ? and enabled = "+trueVal+" and priority = (?)", group, model, maxPrioritySubQuery)
	}
	if common.UsingSQLite || common.UsingPostgreSQL {
		err = channelQuery.Order("RANDOM()").First(&ability).Error
	} else {
		err = channelQuery.Order("RAND()").First(&ability).Error
	}
	if err != nil {
		return nil, err
	}
	channel := Channel{}
	channel.Id = ability.ChannelId
	err = DB.First(&channel, "id = ?", ability.ChannelId).Error
	return &channel, err
}

func (channel *Channel) AddAbilities() error {
	models_ := strings.Split(channel.Models, ",")
	models_ = utils.DeDuplication(models_)
	groups_ := strings.Split(channel.Group, ",")
	abilities := make([]Ability, 0, len(models_))
	for _, model := range models_ {
		for _, group := range groups_ {
			ability := Ability{
				Group:     group,
				Model:     model,
				ChannelId: channel.Id,
				Enabled:   channel.Status == ChannelStatusEnabled,
				Priority:  channel.Priority,
			}
			abilities = append(abilities, ability)
		}
	}
	return DB.Create(&abilities).Error
}

func (channel *Channel) DeleteAbilities() error {
	return DB.Where("channel_id = ?", channel.Id).Delete(&Ability{}).Error
}

// UpdateAbilities updates abilities of this channel.
// Make sure the channel is completed before calling this function.
func (channel *Channel) UpdateAbilities() error {
	// A quick and dirty way to update abilities
	// First delete all abilities of this channel
	err := channel.DeleteAbilities()
	if err != nil {
		return err
	}
	// Then add new abilities
	err = channel.AddAbilities()
	if err != nil {
		return err
	}
	return nil
}

func UpdateAbilityStatus(channelId int, status bool) error {
	return DB.Model(&Ability{}).Where("channel_id = ?", channelId).Select("enabled").Update("enabled", status).Error
}

func GetGroupModels(ctx context.Context, group string) ([]string, error) {
	groupCol := "`group`"
	trueVal := "1"
	if common.UsingPostgreSQL {
		groupCol = `"group"`
		trueVal = "true"
	}
	var models []string
	err := DB.Model(&Ability{}).Distinct("model").Where(groupCol+" = ? and enabled = "+trueVal, group).Pluck("model", &models).Error
	if err != nil {
		return nil, err
	}
	sort.Strings(models)
	return models, err
}
