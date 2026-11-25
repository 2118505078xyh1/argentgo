package session

import (
	"encoding/json"
	"strings"
)

// KindModelRoute 是虚拟模型路由决策的持久记录。它只记录"哪一类任务被路由到
// 哪个物理模型"，不改动历史消息，也不参与授权；重放时可按它审计成本决策。
const KindModelRoute = "model_route"

// ModelRoute 是一条路由决策载荷。
type ModelRoute struct {
	Virtual string `json:"virtual"`
	Task    string `json:"task"`
	Model   string `json:"model"`
}

// AppendModelRoute 把一次路由决策追加到会话。空模型不构成决策，直接返回。
func (s *Session) AppendModelRoute(virtual, task, model string) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}
	_, err := s.AppendRecord(KindModelRoute, ModelRoute{
		Virtual: strings.TrimSpace(virtual), Task: strings.TrimSpace(task), Model: model,
	})
	return err
}

// ScanModelRoutes 从记录中收集路由决策。最后一条相同 virtual+task 的决策胜出，
// 使后续重放能复用同一选择（粘性路由）。
func ScanModelRoutes(records []Record) []ModelRoute {
	index := make(map[string]int)
	routes := make([]ModelRoute, 0)
	for _, record := range records {
		if record.Kind != KindModelRoute {
			continue
		}
		var route ModelRoute
		if err := json.Unmarshal(record.Data, &route); err != nil || route.Model == "" {
			continue
		}
		key := route.Virtual + "\x00" + route.Task
		if i, ok := index[key]; ok {
			routes[i] = route
			continue
		}
		index[key] = len(routes)
		routes = append(routes, route)
	}
	return routes
}

// ModelRoutes 返回当前活动分支上的路由决策视图。
func (s *Session) ModelRoutes() ([]ModelRoute, error) {
	records, err := s.Records()
	if err != nil {
		return nil, err
	}
	return ScanModelRoutes(s.activePath(records)), nil
}
