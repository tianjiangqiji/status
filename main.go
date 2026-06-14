package main

import (
	"bytes"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// FrontendConfig 前端展示配置
type FrontendConfig struct {
	Title    string `json:"title"`
	Icon     string `json:"icon,omitempty"`
	Subtitle string `json:"subtitle,omitempty"`
	Footer   string `json:"footer,omitempty"`
}

// Config 配置文件结构
type Config struct {
	Port     int            `json:"port"`
	Interval int            `json:"interval"`
	Frontend FrontendConfig `json:"frontend"`
	Kuma     KumaConfig     `json:"kuma"`
	Groups   []Group        `json:"groups"`
}

type KumaConfig struct {
	BaseURL string `json:"baseURL"`
	Slug    string `json:"slug"`
}

type Group struct {
	Name      string     `json:"name"`
	SubGroups []SubGroup `json:"subGroups"`
}

type SubGroup struct {
	Name     string  `json:"name"`
	KumaSlug string  `json:"kumaSlug"`
	Models   []Model `json:"models"`
}

type Model struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	BaseURL  string `json:"baseURL"`
	Key      string `json:"key"`
	Timeout  int    `json:"timeout"`
}

type OverallStatus struct {
	Status string `json:"status"`
	Label  string `json:"label"`
}

type GroupResp struct {
	Name          string        `json:"name"`
	SubName       string        `json:"sub_name"`
	Family        string        `json:"family"`
	Status        string        `json:"status"`
	Label         string        `json:"label"`
	HealthScore   float64       `json:"health_score"`
	LatencyMs     int           `json:"latency_ms"`
	LastCheckedAt *time.Time    `json:"last_checked_at,omitempty"`
	History       []HistoryItem `json:"history"`
}

type HistoryItem struct {
	S string  `json:"s"`
	V float64 `json:"v"`
}

// 按外层group.name分组，返回分组列表和该分组下的所有行
type Section struct {
	Name string      `json:"name"`
	Rows []GroupResp `json:"rows"`
}

type StatusRespV2 struct {
	Title           string        `json:"title"`
	Overall         OverallStatus `json:"overall"`
	LastCompletedAt *time.Time    `json:"last_completed_at,omitempty"`
	Checking        bool          `json:"checking"`
	Sections        []Section     `json:"sections"`
}

var (
	cfg      Config
	mu       sync.RWMutex
	statusV2 StatusRespV2
	db       *sql.DB
	checking bool
)

func loadConfig() {
	b, err := os.ReadFile("config.json")
	if err != nil {
		log.Println("config read error:", err)
		return
	}
	var newCfg Config
	if err := json.Unmarshal(b, &newCfg); err != nil {
		log.Println("config parse error:", err)
		return
	}
	if newCfg.Port == 0 {
		newCfg.Port = 8080
	}
	if newCfg.Interval == 0 {
		newCfg.Interval = 60
	}
	if newCfg.Frontend.Title == "" {
		newCfg.Frontend.Title = "Service Status"
	}
	if newCfg.Frontend.Footer == "" {
		newCfg.Frontend.Footer = "Powered by go-status"
	}
	if newCfg.Frontend.Title == "" {
		newCfg.Frontend.Title = "Service Status"
	}
	if newCfg.Frontend.Footer == "" {
		newCfg.Frontend.Footer = "Powered by go-status"
	}
	mu.Lock()
	cfg = newCfg
	mu.Unlock()
}

func initDB() {
	var err error
	db, err = sql.Open("sqlite", "status.db")
	if err != nil {
		log.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS checks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			group_name TEXT NOT NULL,
			sub_name TEXT NOT NULL,
			model_id TEXT NOT NULL,
			up INTEGER NOT NULL,
			latency INTEGER NOT NULL,
			checked_at INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_checks_time ON checks(checked_at);
		CREATE INDEX IF NOT EXISTS idx_checks_group ON checks(group_name, sub_name, model_id, checked_at);
	`)
	if err != nil {
		log.Fatal(err)
	}
}

func saveCheck(groupName, subName, modelID string, up bool, latency int) {
	v := 0
	if up {
		v = 1
	}
	_, err := db.Exec(
		"INSERT INTO checks (group_name, sub_name, model_id, up, latency, checked_at) VALUES (?, ?, ?, ?, ?, ?)",
		groupName, subName, modelID, v, latency, time.Now().Unix(),
	)
	if err != nil {
		log.Println("db insert error:", err)
	}
}

func getHistory(groupName, subName, modelID string, limit int) []HistoryItem {
	rows, err := db.Query(
		"SELECT up, latency FROM checks WHERE group_name = ? AND sub_name = ? AND model_id = ? ORDER BY checked_at DESC LIMIT ?",
		groupName, subName, modelID, limit,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var items []HistoryItem
	for rows.Next() {
		var up int
		var lat int
		rows.Scan(&up, &lat)
		status := "down"
		v := 0.0
		if up == 1 {
			status = "up"
			if lat < 1000 {
				v = 1.0
			} else if lat < 3000 {
				v = 0.7
			} else if lat < 10000 {
				v = 0.4
			} else {
				v = 0.2
			}
		}
		items = append(items, HistoryItem{S: status, V: v})
	}
	// reverse to chronological order
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
	return items
}

func calcHealthScore(groupName, subName, modelID string) float64 {
	rows, err := db.Query(
		"SELECT up FROM checks WHERE group_name = ? AND sub_name = ? AND model_id = ? ORDER BY checked_at DESC LIMIT 100",
		groupName, subName, modelID,
	)
	if err != nil {
		return 0
	}
	defer rows.Close()
	var total, upCount int
	for rows.Next() {
		var up int
		rows.Scan(&up)
		total++
		if up == 1 {
			upCount++
		}
	}
	if total == 0 {
		return 100.0
	}
	return float64(upCount) / float64(total) * 100.0
}

func getLastCheck(groupName, subName, modelID string) (bool, int, time.Time) {
	var up int
	var lat int
	var ts int64
	err := db.QueryRow(
		"SELECT up, latency, checked_at FROM checks WHERE group_name = ? AND sub_name = ? AND model_id = ? ORDER BY checked_at DESC LIMIT 1",
		groupName, subName, modelID,
	).Scan(&up, &lat, &ts)
	if err != nil {
		return false, 0, time.Time{}
	}
	return up == 1, lat, time.Unix(ts, 0)
}

func cleanupOld() {
	// keep last 120 records per sub-group
	db.Exec(`
		DELETE FROM checks WHERE id IN (
			SELECT id FROM checks c1
			WHERE (
				SELECT COUNT(*) FROM checks c2
				WHERE c2.group_name = c1.group_name AND c2.sub_name = c1.sub_name AND c2.model_id = c1.model_id
			) > 120
			AND c1.id NOT IN (
				SELECT id FROM checks c3
				WHERE c3.group_name = c1.group_name AND c3.sub_name = c1.sub_name AND c3.model_id = c1.model_id
				ORDER BY c3.checked_at DESC LIMIT 120
			)
		)
	`)
}

// 四种协议的健康检查，均使用最小token输入输出
func checkOpenAICompat(m Model) (bool, int) {
	body := []byte(fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"h"}],"max_tokens":1}`, m.ID))
	return doPost(m.BaseURL+"/chat/completions", m.Key, body, m.Timeout)
}

func checkOpenAIResponse(m Model) (bool, int) {
	body := []byte(fmt.Sprintf(`{"model":"%s","input":"h","max_tokens":1}`, m.ID))
	return doPost(m.BaseURL+"/responses", m.Key, body, m.Timeout)
}

func checkGoogle(m Model) (bool, int) {
	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", m.BaseURL, m.ID, m.Key)
	body := []byte(`{"contents":[{"parts":[{"text":"h"}]}],"generationConfig":{"maxOutputTokens":1}}`)
	return doPost(url, "", body, m.Timeout)
}

func checkAnthropic(m Model) (bool, int) {
	body := []byte(fmt.Sprintf(`{"model":"%s","max_tokens":1,"messages":[{"role":"user","content":"h"}]}`, m.ID))
	req, err := http.NewRequest("POST", m.BaseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return false, 0
	}
	req.Header.Set("x-api-key", m.Key)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")
	return doReq(req, m.Timeout)
}

func doPost(url, key string, body []byte, timeout int) (bool, int) {
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return false, 0
	}
	req.Header.Set("content-type", "application/json")
	if key != "" {
		req.Header.Set("authorization", "Bearer "+key)
	}
	return doReq(req, timeout)
}

func doReq(req *http.Request, timeout int) (bool, int) {
	if timeout == 0 {
		timeout = 30
	}
	client := &http.Client{
		Timeout: time.Duration(timeout) * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	start := time.Now()
	resp, err := client.Do(req)
	lat := int(time.Since(start).Milliseconds())
	if err != nil {
		return false, lat
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode < 300, lat
}

func checkOne(m Model) (bool, int) {
	switch m.Provider {
	case "openai-compat":
		return checkOpenAICompat(m)
	case "openai-response":
		return checkOpenAIResponse(m)
	case "google":
		return checkGoogle(m)
	case "anthropic":
		return checkAnthropic(m)
	default:
		return checkOpenAICompat(m)
	}
}

func providerFamily(p string) string {
	switch p {
	case "openai-compat", "openai-response":
		return "openai"
	case "anthropic":
		return "claude"
	case "google":
		return "gemini"
	default:
		return "other"
	}
}

// updateRow 检测完一个模型后立即更新共享状态，前端轮询可见
func updateRow(gName string, sg SubGroup, m Model, sections *[]Section, up bool, lat int) {
	statusStr := "down"
	label := "异常"
	if up {
		statusStr = "up"
		label = "正常"
	}
	hist := getHistory(gName, sg.Name, m.ID, 120)
	score := calcHealthScore(gName, sg.Name, m.ID)
	_, _, lastAt := getLastCheck(gName, sg.Name, m.ID)

	row := GroupResp{
		Name:          m.ID,
		SubName:       sg.Name,
		Family:        providerFamily(m.Provider),
		Status:        statusStr,
		Label:         label,
		HealthScore:   score,
		LatencyMs:     lat,
		LastCheckedAt: &lastAt,
		History:       hist,
	}

	mu.Lock()
	*sections = upsertSectionRow(*sections, gName, row)

	// 计算当前整体状态
	allUp := true
	for _, sec := range *sections {
		for _, r := range sec.Rows {
			if r.Status == "down" {
				allUp = false
				break
			}
		}
		if !allUp {
			break
		}
	}
	overallStatus := "up"
	overallLabel := "运行正常"
	if !allUp {
		overallStatus = "degraded"
		overallLabel = "部分异常"
	}

	statusV2 = StatusRespV2{
		Title:    cfg.Frontend.Title,
		Overall:  OverallStatus{Status: overallStatus, Label: overallLabel},
		Checking: true,
		Sections: *sections,
	}
	mu.Unlock()
}

// upsertSectionRow 在 sections 中插入或更新一行，用于逐个检测时实时更新前端
func upsertSectionRow(sections []Section, gName string, row GroupResp) []Section {
	idx := -1
	for i, sec := range sections {
		if sec.Name == gName {
			idx = i
			break
		}
	}
	if idx == -1 {
		return append(sections, Section{Name: gName, Rows: []GroupResp{row}})
	}
	sec := &sections[idx]
	for i, r := range sec.Rows {
		if r.Name == row.Name && r.SubName == row.SubName {
			sec.Rows[i] = row
			return sections
		}
	}
	sec.Rows = append(sec.Rows, row)
	return sections
}

// pruneStaleSections 根据 config 当前条目移除 sections 中已不存在的 group/subGroup/model 行
func pruneStaleSections(sections []Section, cfg Config) []Section {
	// 建立 config 中有效条目的集合: "groupName|subName|modelID"
	valid := make(map[string]bool)
	for _, g := range cfg.Groups {
		for _, sg := range g.SubGroups {
			for _, m := range sg.Models {
				valid[g.Name+"|"+sg.Name+"|"+m.ID] = true
			}
		}
	}

	var result []Section
	for _, sec := range sections {
		var keptRows []GroupResp
		for _, row := range sec.Rows {
			key := sec.Name + "|" + row.SubName + "|" + row.Name
			if valid[key] {
				keptRows = append(keptRows, row)
			}
		}
		if len(keptRows) > 0 {
			result = append(result, Section{Name: sec.Name, Rows: keptRows})
		}
	}
	return result
}

func refresh() {
	loadConfig()
	mu.Lock()
	checking = true
	mu.Unlock()

	// 以当前已有状态为起点，逐个检测并实时更新
	mu.RLock()
	sections := statusV2.Sections
	if sections == nil {
		sections = []Section{}
	}
	mu.RUnlock()

	// 根据 config 清理已不存在的条目
	sections = pruneStaleSections(sections, cfg)

	now := time.Now()
	totalLat := 0
	latCount := 0

	// failRecords 记录首次失败的模型，等全部测完再统一重试
	type failRecord struct {
		gName, sgName string
		m             Model
		lat           int
	}
	var failRecords []failRecord

	// 逐个检测所有模型，失败的暂不记录，先收集
	for _, g := range cfg.Groups {
		for _, sg := range g.SubGroups {
			for _, m := range sg.Models {
				up, lat := checkOne(m)
				if up {
					saveCheck(g.Name, sg.Name, m.ID, true, lat)
					totalLat += lat
					latCount++
					updateRow(g.Name, sg, m, &sections, true, lat)
				} else {
					failRecords = append(failRecords, failRecord{gName: g.Name, sgName: sg.Name, m: m, lat: lat})
				}
			}
		}
	}

	// 等待5秒后重试所有失败的模型
	if len(failRecords) > 0 {
		time.Sleep(5 * time.Second)
		for _, fr := range failRecords {
			up, lat := checkOne(fr.m)
			saveCheck(fr.gName, fr.sgName, fr.m.ID, up, lat)
			if up {
				totalLat += lat
				latCount++
			}
			updateRow(fr.gName, SubGroup{Name: fr.sgName}, fr.m, &sections, up, lat)
		}
	}

	// 计算整体状态
	allUp := true
	for _, sec := range sections {
		for _, r := range sec.Rows {
			if r.Status == "down" {
				allUp = false
				break
			}
		}
		if !allUp {
			break
		}
	}

	// 全部检测完毕，标记 checking = false
	overallStatus := "up"
	overallLabel := "运行正常"
	if !allUp {
		overallStatus = "down"
		overallLabel = "部分异常"
	}

	mu.Lock()
	statusV2 = StatusRespV2{
		Title:           cfg.Frontend.Title,
		Overall:         OverallStatus{Status: overallStatus, Label: overallLabel},
		LastCompletedAt: &now,
		Checking:        false,
		Sections:        sections,
	}
	checking = false
	mu.Unlock()

	// push to Uptime Kuma
	// 1. 推送整体状态
	if cfg.Kuma.BaseURL != "" && cfg.Kuma.Slug != "" {
		avgPing := 0
		if latCount > 0 {
			avgPing = totalLat / latCount
		}
		pingKuma(cfg.Kuma.Slug, allUp, avgPing)
	}
	// 2. 推送每个子组的独立状态
	if cfg.Kuma.BaseURL != "" {
		for _, g := range cfg.Groups {
			for _, sg := range g.SubGroups {
				if sg.KumaSlug != "" {
					var sgUpCount, sgTotal int
					var sgLatSum, sgLatCount int
					for _, m := range sg.Models {
						up, lat, _ := getLastCheck(g.Name, sg.Name, m.ID)
						sgTotal++
						if up {
							sgUpCount++
							sgLatSum += lat
							sgLatCount++
						}
					}
					sgUp := sgUpCount == sgTotal && sgTotal > 0
					sgAvgPing := 0
					if sgLatCount > 0 {
						sgAvgPing = sgLatSum / sgLatCount
					}
					pingKuma(sg.KumaSlug, sgUp, sgAvgPing)
				}
			}
		}
	}

	cleanupOld()
}

func pingKuma(slug string, up bool, pingMs int) {
	if slug == "" {
		return
	}
	url := strings.TrimSuffix(cfg.Kuma.BaseURL, "/") + "/api/push/" + slug
	statusStr := "down"
	if up {
		statusStr = "up"
	}
	pingParam := ""
	if pingMs > 0 {
		pingParam = fmt.Sprintf("%d", pingMs)
	}
	resp, err := http.Get(url + "?status=" + statusStr + "&msg=ok&ping=" + pingParam)
	if err != nil {
		return
	}
	resp.Body.Close()
}

// Kuma 公开状态页兼容结构
type KumaCategoryConfig struct {
	Title       string `json:"title"`
	Slug        string `json:"slug"`
	Description string `json:"description,omitempty"`
}

type KumaCategoryMonitor struct {
	ID      int     `json:"id"`
	Name    string  `json:"name"`
	Type    string  `json:"type"`
	Status  int     `json:"status"` // 0=down 1=up 2=pending
	Latency int     `json:"latency"`
	Msg     string  `json:"msg"`
	Uptime  float64 `json:"uptime"`
}

type KumaCategoryGroup struct {
	ID          int                   `json:"id"`
	Name        string                `json:"name"`
	Weight      int                   `json:"weight"`
	MonitorList []KumaCategoryMonitor `json:"monitorList"`
}

type KumaCategoryResp struct {
	Config          KumaCategoryConfig  `json:"config"`
	PublicGroupList []KumaCategoryGroup `json:"publicGroupList"`
}

// ---- Heartbeat 兼容结构（NewAPI 需要 /api/status-page/heartbeat/:slug）----

type HeartbeatPublic struct {
	Status int    `json:"status"` // 0=down 1=up 2=pending
	Time   string `json:"time"`
	Msg    string `json:"msg"`
	Ping   int    `json:"ping"`
}

// getHeartbeats 查询最近 limit 条心跳记录，返回按时间正序排列
func getHeartbeats(groupName, subName, modelID string, limit int) []HeartbeatPublic {
	rows, err := db.Query(
		"SELECT up, latency, checked_at FROM checks WHERE group_name = ? AND sub_name = ? AND model_id = ? ORDER BY checked_at DESC LIMIT ?",
		groupName, subName, modelID, limit,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var items []HeartbeatPublic
	for rows.Next() {
		var up, lat int
		var ts int64
		rows.Scan(&up, &lat, &ts)
		status := 0
		if up == 1 {
			status = 1
		}
		items = append(items, HeartbeatPublic{
			Status: status,
			Time:   time.Unix(ts, 0).UTC().Format(time.RFC3339),
			Msg:    "",
			Ping:   lat,
		})
	}
	// 反转为时间正序
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
	return items
}

// calcUptime24h 计算 24 小时内的 uptime（0~1）
func calcUptime24h(groupName, subName, modelID string) float64 {
	cutoff := time.Now().Add(-24 * time.Hour).Unix()
	var total, upCount int
	rows, err := db.Query(
		"SELECT up FROM checks WHERE group_name = ? AND sub_name = ? AND model_id = ? AND checked_at >= ?",
		groupName, subName, modelID, cutoff,
	)
	if err != nil {
		return 1.0
	}
	defer rows.Close()
	for rows.Next() {
		var up int
		rows.Scan(&up)
		total++
		if up == 1 {
			upCount++
		}
	}
	if total == 0 {
		return 1.0
	}
	return float64(upCount) / float64(total)
}

// monitorRef 记录一个模型的分组上下文和分配的 monitor ID
type monitorRef struct {
	GroupName string
	SubName   string
	ModelID   string
	MonitorID int
}

// collectMonitorRefs 根据 slug 收集 monitor 引用列表
// overall slug 返回全部模型（全局唯一 ID），子组 slug 返回该子组的模型
func collectMonitorRefs(cfg Config, slug string) []monitorRef {
	var refs []monitorRef

	// 检查是否为 overall slug
	if slug == cfg.Kuma.Slug {
		id := 1
		for _, g := range cfg.Groups {
			for _, sg := range g.SubGroups {
				for _, m := range sg.Models {
					refs = append(refs, monitorRef{g.Name, sg.Name, m.ID, id})
					id++
				}
			}
		}
		return refs
	}

	// 查找子组 slug
	for _, g := range cfg.Groups {
		for _, sg := range g.SubGroups {
			if sg.KumaSlug == slug {
				for i, m := range sg.Models {
					refs = append(refs, monitorRef{g.Name, sg.Name, m.ID, i + 1})
				}
				return refs
			}
		}
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("content-type", "application/json; charset=utf-8")
	w.Header().Set("access-control-allow-origin", "*")
	w.Header().Set("access-control-allow-headers", "*")
	w.WriteHeader(code)
	b, _ := json.Marshal(v)
	w.Write(b)
}

func apiStatus(w http.ResponseWriter, r *http.Request) {
	mu.RLock()
	s := statusV2
	c := checking
	mu.RUnlock()
	s.Checking = c
	writeJSON(w, 200, s)
}

func apiConfig(w http.ResponseWriter, r *http.Request) {
	mu.RLock()
	interval := cfg.Interval
	frontend := cfg.Frontend
	mu.RUnlock()
	writeJSON(w, 200, map[string]interface{}{
		"interval": interval,
		"frontend": frontend,
	})
}

// /api/status-page/:slug — 原版 Uptime Kuma 公开状态页兼容
func apiStatusPage(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimPrefix(r.URL.Path, "/api/status-page/")
	slug = strings.Trim(slug, "/")
	if slug == "" {
		writeJSON(w, 400, map[string]string{"error": "slug required"})
		return
	}

	mu.RLock()
	cfgSnapshot := cfg
	s := statusV2
	mu.RUnlock()

	// 优先匹配顶级 kuma.slug（"总体状况"）—— 返回全量 Kuma 兼容格式
	if slug == cfgSnapshot.Kuma.Slug {
		groups := make([]KumaCategoryGroup, 0)
		groupID := 1
		monitorID := 1 // 全局递增，与 heartbeat 端点一致
		for _, g := range cfgSnapshot.Groups {
			monitors := make([]KumaCategoryMonitor, 0)
			for _, sg := range g.SubGroups {
				// 在 sections 中查找该子组的状态行
				var rows []GroupResp
				for _, sec := range s.Sections {
					if sec.Name == g.Name {
						for _, row := range sec.Rows {
							if row.SubName == sg.Name {
								rows = append(rows, row)
							}
						}
						break
					}
				}
				for _, m := range sg.Models {
					status := 2
					lat := 0
					msg := "等待检测"
					uptime := 0.0
					for j := range rows {
						if rows[j].Name == m.ID {
							switch rows[j].Status {
							case "up":
								status = 1
								msg = "运行正常"
							case "down":
								status = 0
								msg = "服务异常"
							default:
								status = 2
								msg = "采样中"
							}
							lat = rows[j].LatencyMs
							uptime = rows[j].HealthScore
							break
						}
					}
					monitors = append(monitors, KumaCategoryMonitor{
						ID:      monitorID,
						Name:    sg.Name,
						Type:    "push",
						Status:  status,
						Latency: lat,
						Msg:     msg,
						Uptime:  uptime,
					})
					monitorID++
				}
			}
			if len(monitors) > 0 {
				groups = append(groups, KumaCategoryGroup{
					ID:          groupID,
					Name:        g.Name,
					Weight:      groupID,
					MonitorList: monitors,
				})
				groupID++
			}
		}
		writeJSON(w, 200, map[string]interface{}{
			"config": KumaCategoryConfig{
				Title:       "总体状况",
				Slug:        slug,
				Description: "全量模型状态",
			},
			"publicGroupList": groups,
		})
		return
	}

	// 找到 kumaSlug 匹配的子组
	var groupName, subName string
	var models []Model
	found := false
	for _, g := range cfgSnapshot.Groups {
		for _, sg := range g.SubGroups {
			if sg.KumaSlug == slug {
				groupName = g.Name
				subName = sg.Name
				models = sg.Models
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		writeJSON(w, 404, map[string]string{"error": "category not found: " + slug})
		return
	}

	// 在 statusV2.Sections 里找该子组的行
	var rows []GroupResp
	for _, sec := range s.Sections {
		if sec.Name == groupName {
			for _, row := range sec.Rows {
				if row.SubName == subName {
					rows = append(rows, row)
				}
			}
			break
		}
	}

	// 转换为 Kuma 兼容格式（0=down 1=up 2=pending）
	monitors := make([]KumaCategoryMonitor, 0, len(models))
	for i, m := range models {
		status := 2
		lat := 0
		msg := "等待检测"
		uptime := 0.0
		for j := range rows {
			if rows[j].Name == m.ID {
				switch rows[j].Status {
				case "up":
					status = 1
					msg = "运行正常"
				case "down":
					status = 0
					msg = "服务异常"
				default:
					status = 2
					msg = "采样中"
				}
				lat = rows[j].LatencyMs
				uptime = rows[j].HealthScore
				break
			}
		}
		monitors = append(monitors, KumaCategoryMonitor{
			ID:      i + 1,
			Name:    subName,
			Type:    "push",
			Status:  status,
			Latency: lat,
			Msg:     msg,
			Uptime:  uptime,
		})
	}

	resp := KumaCategoryResp{
		Config: KumaCategoryConfig{
			Title:       subName,
			Slug:        slug,
			Description: groupName,
		},
		PublicGroupList: []KumaCategoryGroup{
			{
				ID:          1,
				Name:        subName,
				Weight:      1,
				MonitorList: monitors,
			},
		},
	}
	writeJSON(w, 200, resp)
}

// /api/status-page/heartbeat/:slug — NewAPI 需要的 heartbeat 轮询端点
func apiStatusPageHeartbeat(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimPrefix(r.URL.Path, "/api/status-page/heartbeat/")
	slug = strings.Trim(slug, "/")
	if slug == "" {
		writeJSON(w, 400, map[string]string{"error": "slug required"})
		return
	}

	mu.RLock()
	cfgSnapshot := cfg
	mu.RUnlock()

	refs := collectMonitorRefs(cfgSnapshot, slug)
	if refs == nil {
		writeJSON(w, 404, map[string]string{"error": "category not found: " + slug})
		return
	}

	heartbeatList := make(map[string][]HeartbeatPublic)
	uptimeList := make(map[string]float64)

	for _, ref := range refs {
		monitorIDStr := strconv.Itoa(ref.MonitorID)
		hbs := getHeartbeats(ref.GroupName, ref.SubName, ref.ModelID, 100)
		if hbs == nil {
			hbs = []HeartbeatPublic{}
		}
		heartbeatList[monitorIDStr] = hbs
		uptimeList[monitorIDStr+"_24"] = calcUptime24h(ref.GroupName, ref.SubName, ref.ModelID)
	}

	writeJSON(w, 200, map[string]interface{}{
		"heartbeatList": heartbeatList,
		"uptimeList":    uptimeList,
	})
}

func main() {
	loadConfig()
	initDB()

	go func() {
		refresh()
		for {
			mu.RLock()
			sec := cfg.Interval
			mu.RUnlock()
			time.Sleep(time.Duration(sec) * time.Second)
			refresh()
		}
	}()

	http.HandleFunc("/api/status", apiStatus)
	http.HandleFunc("/api/config", apiConfig)
	http.HandleFunc("/api/status-page/heartbeat/", apiStatusPageHeartbeat)
	http.HandleFunc("/api/status-page/", apiStatusPage)
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "status.html")
	})

	addr := fmt.Sprintf(":%d", cfg.Port)
	log.Println("status monitor on", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
