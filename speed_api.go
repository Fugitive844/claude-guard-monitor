package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

func (a *App) speedAPI(w http.ResponseWriter, r *http.Request) {
	s := a.speed
	if s == nil {
		respond(w, 503, map[string]string{"error": "测速服务未就绪"})
		return
	}
	method := "GET"
	if r.URL.Path == "/api/speed/start" || r.URL.Path == "/api/speed/stop" {
		method = "POST"
	}
	if r.URL.Path == "/api/speed/settings" && r.Method == "POST" {
		method = "POST"
	}
	if r.Method != method {
		w.WriteHeader(405)
		return
	}
	switch r.URL.Path {
	case "/api/speed/targets":
		respond(w, 200, map[string]any{"items": speedTargets})
	case "/api/speed/nodes":
		v, err := s.nodes(r.Context())
		if err != nil {
			respond(w, 503, map[string]string{"error": err.Error()})
			return
		}
		latest, err := a.db.latestSpeed(v, false)
		if err != nil {
			respond(w, 500, map[string]string{"error": "读取最近测速结果失败"})
			return
		}
		previous, err := a.db.latestSpeed(v, true)
		if err != nil {
			respond(w, 500, map[string]string{"error": "读取有效测速结果失败"})
			return
		}
		respond(w, 200, map[string]any{"items": v, "latest": latest, "previous": previous})
	case "/api/speed/settings":
		if r.Method == "GET" {
			s.mu.Lock()
			cfg := s.settings
			s.mu.Unlock()
			respond(w, 200, cfg)
			return
		}
		cfg := defaultSpeedSettings()
		if decodeBody(w, r, &cfg) != nil {
			respond(w, 400, map[string]string{"error": "测速设置格式错误"})
			return
		}
		if err := cfg.validate(); err != nil {
			respond(w, 400, map[string]string{"error": err.Error()})
			return
		}
		s.mu.Lock()
		b, _ := json.MarshalIndent(cfg, "", "  ")
		err := writeAtomic(s.settingsPath(), b, 0600)
		if err == nil {
			s.settings = cfg
		}
		s.mu.Unlock()
		if err != nil {
			respond(w, 500, map[string]string{"error": "保存测速设置失败"})
			return
		}
		respond(w, 200, map[string]bool{"ok": true})
	case "/api/speed/start":
		var req SpeedRequest
		if decodeBody(w, r, &req) != nil {
			respond(w, 400, map[string]string{"error": "测速请求格式错误"})
			return
		}
		nodes, err := s.nodes(r.Context())
		if err == nil {
			err = s.start(req, nodes)
		}
		if err != nil {
			respond(w, 409, map[string]string{"error": err.Error()})
			return
		}
		respond(w, 202, a.speedStatus())
	case "/api/speed/stop":
		s.stop()
		respond(w, 200, a.speedStatus())
	case "/api/speed/status":
		respond(w, 200, a.speedStatus())
	case "/api/speed/history":
		from, to, before, err := queryRange(r.URL.Query(), time.Now().UnixMilli())
		if err != nil {
			respond(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if r.URL.Query().Get("grouped") == "1" {
			groups, err := a.db.speedHistoryGroups(from, to, before, r.URL.Query().Get("node_id"), r.URL.Query().Get("scope"))
			if err != nil {
				respond(w, 500, map[string]string{"error": "读取测速历史失败"})
				return
			}
			more := len(groups) > 50
			var next int64
			if more {
				groups = groups[:50]
				next = groups[49].ID
			}
			respond(w, 200, map[string]any{"items": groups, "has_more": more, "next_before": next})
			return
		}
		items, err := a.db.speedHistory(from, to, before, r.URL.Query().Get("node_id"), r.URL.Query().Get("scope"))
		if err != nil {
			respond(w, 500, map[string]string{"error": "读取测速历史失败"})
			return
		}
		more := len(items) > 50
		var next int64
		if more {
			items = items[:50]
			next = items[49].ID
		}
		respond(w, 200, map[string]any{"items": items, "has_more": more, "next_before": next})
	}
}

func (s *Store) saveSpeed(r *SpeedResult) error {
	summary := *r
	summary.Samples = nil
	summary.IntervalMbps = nil
	b, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	result, err := s.Exec("INSERT INTO speed_results(ts,result) VALUES(?,?)", r.Time, string(b))
	if err != nil {
		return err
	}
	r.ID, err = result.LastInsertId()
	return err
}
func (s *Store) speedHistory(from, to, before int64, nodeID string, scope ...string) ([]SpeedResult, error) {
	if before == 0 {
		before = 1 << 62
	}
	query, args := speedHistoryFilter(from, to, nodeID, scope...)
	args = append(args, before)
	return s.querySpeed("SELECT id,result FROM speed_results"+query+" AND id<? ORDER BY id DESC LIMIT 51", args...)
}

func speedHistoryFilter(from, to int64, nodeID string, scope ...string) (string, []any) {
	query := " WHERE ts>=? AND ts<?"
	args := []any{from, to}
	if nodeID != "" {
		if strings.HasPrefix(nodeID, "ssh:") {
			query += " AND (json_extract(result,'$.node_id')=? OR json_extract(result,'$.source_host_id')=?)"
			args = append(args, nodeID, strings.TrimPrefix(nodeID, "ssh:"))
		} else {
			query += " AND json_extract(result,'$.node_id')=?"
			args = append(args, nodeID)
		}
	}
	if len(scope) > 0 {
		switch scope[0] {
		case "ssh":
			query += " AND json_extract(result,'$.node_id') LIKE 'ssh:%'"
		case "proxy":
			query += " AND json_extract(result,'$.node_id') NOT LIKE 'ssh:%'"
		}
	}
	return query, args
}

type speedHistoryGroup struct {
	ID    int64         `json:"id"`
	Items []SpeedResult `json:"items"`
}

func (s *Store) speedHistoryGroups(from, to, before int64, nodeID, scope string) ([]speedHistoryGroup, error) {
	if before == 0 {
		before = 1 << 62
	}
	filter, args := speedHistoryFilter(from, to, nodeID, scope)
	// Missing job identifiers stay separate; timestamps cannot establish a round.
	key := `json_array(COALESCE(NULLIF(json_extract(result,'$.job'),''),id),json_extract(result,'$.node_id'),COALESCE(json_extract(result,'$.source_host_id'),''),COALESCE(json_extract(result,'$.mode'),''),COALESCE(json_extract(result,'$.protocol'),''),COALESCE(json_extract(result,'$.test_config.port'),0))`
	query := `WITH eligible AS (SELECT id,result,` + key + ` AS round_key FROM speed_results` + filter + `),
 selected AS (SELECT round_key,MAX(id) AS last_id FROM eligible GROUP BY round_key HAVING MAX(id)<? ORDER BY last_id DESC LIMIT 51)
 SELECT e.id,e.result,g.last_id FROM eligible e JOIN selected g ON e.round_key=g.round_key ORDER BY g.last_id DESC,e.id DESC`
	rows, err := s.Query(query, append(args, before)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := []speedHistoryGroup{}
	for rows.Next() {
		var r SpeedResult
		var id, groupID int64
		var raw string
		if err = rows.Scan(&id, &raw, &groupID); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &r); err != nil {
			return nil, err
		}
		r.ID = id
		if len(groups) == 0 || groups[len(groups)-1].ID != groupID {
			groups = append(groups, speedHistoryGroup{ID: groupID, Items: []SpeedResult{}})
		}
		g := &groups[len(groups)-1]
		g.Items = append(g.Items, r)
	}
	return groups, rows.Err()
}

func (s *Store) latestSpeed(nodes []SpeedNode, measuredOnly bool) ([]SpeedResult, error) {
	if len(nodes) == 0 {
		return []SpeedResult{}, nil
	}
	args := make([]any, len(nodes))
	for i, node := range nodes {
		args[i] = node.ID
	}
	filter, group := "", "json_extract(result,'$.node_id'),json_extract(result,'$.direction')"
	if measuredOnly {
		filter = " AND json_extract(result,'$.direction') IN ('download','upload') AND (json_extract(result,'$.status') IN ('complete','limited') OR (json_extract(result,'$.status')='cancelled' AND json_extract(result,'$.bytes')>0 AND json_extract(result,'$.seconds')>0))"
		group += ",COALESCE(json_extract(result,'$.source_host_id'),''),COALESCE(json_extract(result,'$.mode'),''),COALESCE(json_extract(result,'$.protocol'),''),COALESCE(json_extract(result,'$.test_config.port'),0)"
	}
	query := "SELECT id,result FROM speed_results WHERE id IN (SELECT MAX(id) FROM speed_results WHERE json_extract(result,'$.node_id') IN (" + strings.TrimSuffix(strings.Repeat("?,", len(nodes)), ",") + ")" + filter + " GROUP BY " + group + ")"
	return s.querySpeed(query, args...)
}

func (s *Store) querySpeed(query string, args ...any) ([]SpeedResult, error) {
	rows, err := s.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []SpeedResult{}
	for rows.Next() {
		var id int64
		var b string
		if err = rows.Scan(&id, &b); err != nil {
			return nil, err
		}
		var r SpeedResult
		if err = json.Unmarshal([]byte(b), &r); err != nil {
			return nil, err
		}
		r.ID = id
		items = append(items, r)
	}
	return items, rows.Err()
}
