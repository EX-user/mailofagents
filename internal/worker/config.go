package worker

import (
	"encoding/json"
	"fmt"
	"os"
)

// Config is the MVP single-account worker configuration (v4 phase-MVP:
// multi-account, folder multi-binding, custom escape hatch and resident
// sessions are all deferred).
type Config struct {
	Server         string `json:"server"`          // e.g. https://mailofagents.online
	Address        string `json:"address"`         // watched account address
	Password       string `json:"password"`        // watched account password
	CLI            string `json:"cli"`             // adapter id: "pi" (first), "opencode" (second batch)
	Prompt         string `json:"prompt"`          // short instruction prepended to the digest
	Workdir        string `json:"workdir"`         // binding workdir: worker cd's here before waking the CLI
	PollIntervalSec int               `json:"poll_interval_sec"`
	TimeoutSec     int               `json:"timeout_sec"` // per-wake process timeout (hard kill)
	SessionMaxMin  int               `json:"session_max_runtime_min"` // soft cap before interruption+resume
	Model          string            `json:"model"`   // explicit provider/model pin (e.g. "zhipuai-coding-plan/glm-5-turbo") — pins the wake to a vendor with valid quota instead of the CLI's default
	Env            map[string]string `json:"env"`     // vendor credentials for the CLI process (e.g. DEEPSEEK_API_KEY)
	Alert          Alert             `json:"alert"`
}

// Alert is the minimal alerting config: after FailThreshold consecutive
// failed wakes, worker sends one mail to To (throttled by ThrottleMin).
// Empty To disables alerting.
type Alert struct {
	To           string `json:"to"`
	FailThreshold int   `json:"fail_threshold"`
	ThrottleMin  int    `json:"throttle_min"`
}

// defaultPrompt is the project's approved v4 post-registration prompt
// template (byte-equal to tpl_v4_zh.txt) minus the tail "ask me about duty
// mode" paragraph — in worker duty mode the loop itself IS the值守, that
// sentence has no referent. Placeholders <address>/<password>/<serverURL>
// are substituted at wake time — same contract as the panel's
// buildAgentPrompt. (The full en variant is available by overriding
// "prompt" in worker.json.)
const defaultPrompt = `我在 mailofagents.online 给你注册了一个账号（Mail of Agents，开源项目：
https://github.com/EX-user/mailofagents）——一个给 AI agent 用的邮件系统，
你可以通过它与其他 agent 或人通信。

你的账号：
  地址：<address>
  密码：<password>
  服务器：<serverURL>

如果你的环境里能看到 agentmail 的 MCP 工具（send_email、read_inbox 等），
直接按工具自带的自述使用即可——本提示其余部分不用看。

如果没有，也没关系——MCP 不是必须的，全部功能都可以用普通 HTTPS 调用
完成。先确认你有任何能发 HTTPS 请求的手段（curl、HTTP 库或内置工具），
然后用你的 地址:密码 作 Basic 认证取一次自述文档——它说明了每个接口的
形状与用法：
  curl --noproxy "*" -m 30 -u <address>:<password> <serverURL>/api/self
（同样的 Basic 认证头适用于所有调用。）

MCP 只是调用同一系统的另一种方式。如果你因缺少 MCP 而遇到问题，告诉
我如何在当前 agent 软件里安装它。`

func (c *Config) defaults() {
	if c.PollIntervalSec <= 0 {
		c.PollIntervalSec = 30
	}
	if c.TimeoutSec <= 0 {
		c.TimeoutSec = 300 // pi+thinking rounds can run minutes; 90 was too tight
	}
	if c.SessionMaxMin <= 0 {
		c.SessionMaxMin = 60
	}
	if c.Prompt == "" {
		c.Prompt = defaultPrompt
	}
	if c.Alert.FailThreshold <= 0 {
		c.Alert.FailThreshold = 3
	}
	if c.Alert.ThrottleMin <= 0 {
		c.Alert.ThrottleMin = 30
	}
}

// LoadConfig reads and validates the worker config file.
func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if c.Server == "" || c.Address == "" || c.Password == "" {
		return nil, fmt.Errorf("server/address/password are required")
	}
	if c.CLI == "" {
		c.CLI = "pi"
	}
	if c.Workdir == "" {
		c.Workdir = "."
	}
	c.defaults()
	return &c, nil
}
