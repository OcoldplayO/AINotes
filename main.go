package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"os" // 👈 引入操作系统读写文件包
	"strings"
	"syscall"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

// 【配置结构体】
type Config struct {
	ApiKey  string `json:"api_key"`
	BaseUrl string `json:"base_url"`
	Model   string `json:"model"`
	Port    int    `json:"port"`
}

type Note struct {
	ID        int
	Title     string
	Content   string
	AiSummary string
	CreatedAt string
}

// 全局变量保存配置
var cfg Config
var db *sql.DB
var tmpl = template.Must(template.New("index").Parse(tmplStr))

// 【新增核心函数】启动时自动加载或生成本地配置文件
func loadConfig() {
	configPath := "config.json"
	
	// 检查文件是否存在
	data, err := os.ReadFile(configPath)
	if err != nil {
		// 如果文件不存在，自动生成一个带默认值的模板配置文件
		defaultCfg := Config{
			ApiKey:  "请在此填入你在智谱控制台申请的API_KEY",
			BaseUrl: "https://open.bigmodel.cn/api/paas/v4/chat/completions",
			Model:   "glm-4.7-flash",
			Port:    8081,
		}
		// 格式化输出为漂亮的 JSON 格式
		jsonData, _ := json.MarshalIndent(defaultCfg, "", "  ")
		_ = os.WriteFile(configPath, jsonData, 0644)
		
		fmt.Println("==================================================")
		fmt.Println("⚠️  已在当前目录自动为你生成 [config.json] 配置文件！")
		fmt.Println("👉 请填入你的 API Key 并保存该文件后，重新运行 go run main.go")
		fmt.Println("==================================================")
		os.Exit(0) // 优雅退出程序
	}

	// 解析本地 JSON 配置文件到全局变量 cfg 中
	err = json.Unmarshal(data, &cfg)
	if err != nil {
		log.Fatal("解析 config.json 失败，请检查格式是否正确:", err)
	}
	if cfg.Port == 0 {
		cfg.Port = 8081
	}
}

func startServer(port int) {
	addr := fmt.Sprintf(":%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if isPortInUse(err) {
			fmt.Println("==================================================")
			fmt.Printf("❌ 端口 %d 已被占用，无法启动服务！\n", port)
			fmt.Println("👉 常见原因：上次运行的程序还在后台（如 ainotes.exe）")
			fmt.Println("👉 解决方法（任选其一）：")
			fmt.Println("   1. 关闭旧进程：taskkill /IM ainotes.exe /F")
			fmt.Printf("   2. 在 config.json 中把 port 改成其他端口（如 %d）\n", port+1)
			fmt.Println("==================================================")
		}
		log.Fatal("启动 HTTP 服务失败:", err)
	}

	fmt.Println("==================================================")
	fmt.Printf("🚀 服务已成功启动，请在浏览器访问 http://localhost:%d\n", port)
	fmt.Println("==================================================")
	log.Fatal(http.Serve(ln, nil))
}

func isPortInUse(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		var sysErr syscall.Errno
		if errors.As(opErr.Err, &sysErr) && sysErr == syscall.EADDRINUSE {
			return true
		}
	}
	msg := err.Error()
	return strings.Contains(msg, "address already in use") ||
		strings.Contains(msg, "Only one usage of each socket address")
}

func main() {
	// 1. 优先加载本地配置
	loadConfig()

	var err error
	db, err = sql.Open("sqlite3", "./notes.db")
	if err != nil {
		log.Fatal("数据库连接失败:", err)
	}
	defer db.Close()

	// 2. 自动建表（加入 ai_summary 列）
	createTableSQL := `CREATE TABLE IF NOT EXISTS notes (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		title TEXT,
		content TEXT,
		ai_summary TEXT DEFAULT '',
		created_at DATETIME
	);`
	_, err = db.Exec(createTableSQL)
	if err != nil {
		log.Fatal("建表失败:", err)
	}

	// 兼容性升级
	_, _ = db.Exec("ALTER TABLE notes ADD COLUMN ai_summary TEXT DEFAULT '';")

	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/add", handleAdd)

	startServer(cfg.Port)
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query("SELECT id, title, content, ai_summary, created_at FROM notes ORDER BY id DESC")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	notes := []Note{}
	for rows.Next() {
		var n Note
		var createdAt time.Time
		err = rows.Scan(&n.ID, &n.Title, &n.Content, &n.AiSummary, &createdAt)
		if err != nil {
			log.Println("[WARNING] 扫描数据失败:", err)
			continue
		}
		n.CreatedAt = createdAt.Format("2006-01-02 15:04:05")
		notes = append(notes, n)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = tmpl.Execute(w, notes)
}

func handleAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		title := r.FormValue("title")
		content := r.FormValue("content")
		log.Printf("[INFO] 收到新笔记: 标题[%s]", title)

		aiResponse := ""
		// 使用从本地 config.json 读取到的配置
		if cfg.ApiKey != "请在此填入你在智谱控制台申请的API_KEY" && cfg.ApiKey != "" {
			log.Println("[AI] 正在召唤智谱 AI 助手进行深度脑补...")
			var err error
			aiResponse, err = callAI(title, content)
			if err != nil {
				log.Println("[ERROR] 智谱 AI 调用失败:", err)
				aiResponse = "（AI 助手开小差了，暂时无法生成分析）"
			} else {
				log.Println("[SUCCESS] 智谱 AI 脑补完成！")
			}
		} else {
			aiResponse = "（未配置 API_KEY，AI 助手正处于离线模式）"
		}

		_, err := db.Exec("INSERT INTO notes (title, content, ai_summary, created_at) VALUES (?, ?, ?, ?)", title, content, aiResponse, time.Now())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func callAI(title, content string) (string, error) {
	type Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	type RequestBody struct {
		Model    string    `json:"model"`
		Messages []Message `json:"messages"`
	}

	prompt := fmt.Sprintf("请根据我写下的这篇笔记标题《%s》和内容「%s」，帮我做两件事：1. 用20字以内精炼出核心摘要。2. 给出一句延伸思考或启发建议。请用口语化、温暖的语调回答，并用 emoji 表情润色。", title, content)

	// 使用从 cfg 变量中动态读取的值
	reqBody := RequestBody{
		Model: cfg.Model,
		Messages: []Message{
			{Role: "system", Content: "你是一个充满智慧和温度的 AI 笔记整理助手。"},
			{Role: "user", Content: prompt},
		},
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", cfg.BaseUrl, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.ApiKey)

	client := &http.Client{Timeout: 15 * time.Second} 
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	type ResponseBody struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
	}
	var resBody ResponseBody
	err = json.Unmarshal(bodyBytes, &resBody)
	if err != nil {
		return "", fmt.Errorf("解析失败: %v, 原始数据: %s", err, string(bodyBytes))
	}

	if len(resBody.Choices) > 0 {
		return resBody.Choices[0].Message.Content, nil
	}
	return "", fmt.Errorf("未获取到智谱大模型回复内容")
}

const tmplStr = `
<!DOCTYPE html>
<html>
<head>
	<meta charset="utf-8">
	<title>我的本地 AI 笔记库</title>
	<style>
		body { font-family: sans-serif; max-width: 800px; margin: 40px auto; padding: 0 20px; background-color: #f5f5f5; }
		h1 { color: #333; }
		.form-group { margin-bottom: 15px; }
		input[type="text"], textarea { width: 100%; padding: 10px; border: 1px solid #ddd; border-radius: 4px; box-sizing: border-box; }
		button { background-color: #007bff; color: white; border: none; padding: 10px 20px; border-radius: 4px; cursor: pointer; }
		button:hover { background-color: #0056b3; }
		.note-card { background: white; padding: 20px; margin-top: 20px; border-radius: 4px; box-shadow: 0 2px 4px rgba(0,0,0,0.1); }
		.note-title { margin-top: 0; color: #333; }
		.note-meta { color: #888; font-size: 11px; margin-top: 15px; }
		.ai-section { background-color: #f0f7ff; border-left: 4px solid #007bff; padding: 15px; margin-top: 15px; border-radius: 0 4px 4px 0; font-size: 14px; color: #444; line-height: 1.5; }
	</style>
</head>
<body>
	<h1>📝 我的本地 AI 笔记库</h1>
	
	<form action="/add" method="POST">
		<div class="form-group">
			<input type="text" name="title" placeholder="输入笔记标题..." required>
		</div>
		<div class="form-group">
			<textarea name="content" rows="5" placeholder="今天有什么新积累或想法..." required></textarea>
		</div>
		<button type="submit">保存笔记</button>
	</form>

	<hr style="margin: 40px 0; border: 0; border-top: 1px solid #ccc;">

	<h2>📚 已保存的笔记 ({{len .}})</h2>
	{{range .}}
	<div class="note-card">
		<h3 class="note-title">📌 {{.Title}}</h3>
		<p style="white-space: pre-wrap; color: #555;">{{.Content}}</p>
		
		<div class="ai-section">
			<strong>🤖 智谱 AI 脑补分析：</strong><br>
			{{.AiSummary}}
		</div>

		<div class="note-meta">保存时间：{{.CreatedAt}}</div>
	</div>
	{{else}}
	<p style="color: #888;">暂无笔记，开始写下你的第一篇积累吧！</p>
	{{end}}
</body>
</html>
`