package worker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config is the runtime configuration for ONE watched account (the duty
// loop and adapters see exactly this). It is produced by merging a global
// fileConfig with one agent entry (agent wins per-field).
//
// Vendor credentials are NOT managed here — each CLI keeps its own native
// config (opencode: auth.json + opencode.json; pi: ~/.pi/agent; claude:
// login or ANTHROPIC_* env via "env"; codex: ~/.codex/config.toml). The
// worker only pins WHICH model serves the duty via "model" when set.
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
	Model          string            `json:"model"`   // explicit model pin (e.g. "zhipuai-coding-plan/glm-5-turbo") — pins the wake to a model with valid quota instead of the CLI's default
	Env            map[string]string `json:"env"`     // extra env for the CLI process (e.g. DEEPSEEK_API_KEY / ANTHROPIC_*)
	StateFile      string            `json:"state_file"` // session binding store; default = config sibling (<config>.state.json). Kept OUT of the workdir: the workdir is the agent's turf
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

// LoadConfigs reads a worker config file and returns one runtime Config per
// watched account. Two shapes are accepted:
//
//  1. legacy flat single-account (address/password/cli/workdir at top
//     level) — returned as a one-element list;
//  2. multi-account: global fields at top level + "agents" array; each
//     agent entry may override any global field per-field (empty = inherit
//     global).
//
// select limits the result to the matching agent (address prefix, address
// local-part, or 1-based index); empty select returns all.
func LoadConfigs(path, select_ string) ([]*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f fileConfig
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	dir := filepath.Dir(path)
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))

	var out []*Config
	switch {
	case len(f.Agents) > 0:
		for i := range f.Agents {
			ag := &f.Agents[i]
			if ag.Address == "" || ag.Password == "" {
				return nil, fmt.Errorf("agents[%d]: address and password are required", i)
			}
			c := f.globalRuntime(path)
			c.Address = ag.Address
			c.Password = ag.Password
			if ag.CLI != "" {
				c.CLI = ag.CLI
			}
			if ag.Workdir != "" {
				c.Workdir = ag.Workdir
			}
			if ag.Model != "" {
				c.Model = ag.Model
			}
			c.Env = mergeEnv(f.Env, ag.Env)
			if ag.StateFile != "" {
				c.StateFile = ag.StateFile
			} else {
				c.StateFile = filepath.Join(dir, base+"."+localPart(ag.Address)+".state.json")
			}
			c.Alert = mergeAlert(f.Alert, ag.Alert)
			if ag.Server != "" {
				c.Server = ag.Server
			}
			if ag.PollIntervalSec > 0 {
				c.PollIntervalSec = ag.PollIntervalSec
			}
			if ag.TimeoutSec > 0 {
				c.TimeoutSec = ag.TimeoutSec
			}
			if ag.SessionMaxMin > 0 {
				c.SessionMaxMin = ag.SessionMaxMin
			}
			out = append(out, c)
		}
	case f.Address != "":
		c := f.globalRuntime(path)
		c.Address = f.Address
		c.Password = f.Password
		if f.CLI != "" {
			c.CLI = f.CLI
		}
		if f.Workdir != "" {
			c.Workdir = f.Workdir
		}
		out = append(out, c)
	default:
		return nil, fmt.Errorf("%s: no agents and no legacy single-account fields", path)
	}

	if select_ != "" {
		var picked []*Config
		for _, c := range out {
			lp := localPart(c.Address)
			if lp == select_ || strings.HasPrefix(c.Address, select_) || select_ == fmt.Sprint(indexOf(out, c)+1) {
				picked = append(picked, c)
			}
		}
		if len(picked) == 0 {
			return nil, fmt.Errorf("-agent %q matches none of the %d configured accounts", select_, len(out))
		}
		out = picked
	}
	for _, c := range out {
		c.defaults()
	}
	return out, nil
}

func indexOf(list []*Config, c *Config) int {
	for i := range list {
		if list[i] == c {
			return i
		}
	}
	return -1
}

func localPart(addr string) string {
	if i := strings.Index(addr, "@"); i > 0 {
		return addr[:i]
	}
	return addr
}

func mergeEnv(global, override map[string]string) map[string]string {
	m := map[string]string{}
	for k, v := range global {
		m[k] = v
	}
	for k, v := range override {
		m[k] = v
	}
	return m
}

func mergeAlert(global, override Alert) Alert {
	out := global
	if override.To != "" {
		out.To = override.To
	}
	if override.FailThreshold > 0 {
		out.FailThreshold = override.FailThreshold
	}
	if override.ThrottleMin > 0 {
		out.ThrottleMin = override.ThrottleMin
	}
	return out
}

// fileConfig mirrors the JSON file: global fields, the legacy flat
// single-account fields, and the agents list.
type fileConfig struct {
	// global (inherited by every agent unless overridden)
	Server          string `json:"server"`
	PollIntervalSec int    `json:"poll_interval_sec"`
	TimeoutSec      int    `json:"timeout_sec"`
	SessionMaxMin   int    `json:"session_max_runtime_min"`
	Model           string `json:"model"`
	Env             map[string]string `json:"env"`
	StateFile       string `json:"state_file"`
	Alert           Alert  `json:"alert"`

	// legacy flat single-account shape
	Address  string `json:"address"`
	Password string `json:"password"`
	CLI      string `json:"cli"`
	Workdir  string `json:"workdir"`

	// multi-account list
	Agents []agentConfig `json:"agents"`
}

type agentConfig struct {
	Address        string            `json:"address"`
	Password       string            `json:"password"`
	CLI            string            `json:"cli"`
	Workdir        string            `json:"workdir"`
	Model          string            `json:"model"`
	Env            map[string]string `json:"env"`
	StateFile      string            `json:"state_file"`
	Server         string            `json:"server"`
	PollIntervalSec int              `json:"poll_interval_sec"`
	TimeoutSec     int               `json:"timeout_sec"`
	SessionMaxMin  int               `json:"session_max_runtime_min"`
	Alert          Alert             `json:"alert"`
}

// globalRuntime builds a runtime Config prefilled with the global fields.
func (f *fileConfig) globalRuntime(path string) *Config {
	c := &Config{
		Server:          f.Server,
		PollIntervalSec: f.PollIntervalSec,
		TimeoutSec:      f.TimeoutSec,
		SessionMaxMin:   f.SessionMaxMin,
		Model:           f.Model,
		Env:             mergeEnv(f.Env, nil),
		StateFile:       f.StateFile,
		Alert:           f.Alert,
	}
	// legacy flat fields double as global defaults for the single-account
	// shape; for the list shape cli/workdir still make sense as defaults.
	if c.CLI == "" {
		c.CLI = f.CLI
	}
	if f.StateFile == "" {
		ext := filepath.Ext(path)
		c.StateFile = strings.TrimSuffix(path, ext) + ".state.json"
	}
	return c
}
