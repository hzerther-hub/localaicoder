// Package products 产品 profile 机制：单内核多产品（对译 Python products/__init__.py）。
//
// products/<name>/profile.json 定义一个产品的品牌与功能开关；运行时用
// 环境变量 LOCAL_AI_PRODUCT 选择，缺省 devtool_local。
package products

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// DefaultProduct 缺省产品。
const DefaultProduct = "devtool_local"

// EnvKey 产品选择环境变量。
const EnvKey = "LOCAL_AI_PRODUCT"

// KnownFeatures 已知功能开关（缺省 true）。
var KnownFeatures = []string{
	"gpulocal", "dispatch", "editor", "voice", "mcp",
	"attachments", "sessions", "quant", "rag", "zh_only", "quant_tools",
}

// Profile 一个产品的品牌与功能开关。
type Profile struct {
	Name     string
	Title    string
	Features map[string]bool
	ExeName  string
}

// Feature 查询功能开关；缺失键缺省 def。
func (p *Profile) Feature(key string, def bool) bool {
	if v, ok := p.Features[key]; ok {
		return v
	}
	return def
}

var (
	mu     sync.Mutex
	active *Profile
)

// Dir products 目录解析：环境变量覆盖 → 工作目录/products → exe 旁/products。
func Dir() string {
	if d := os.Getenv("LAS_PRODUCTS_DIR"); d != "" {
		return d
	}
	if _, err := os.Stat("products"); err == nil {
		return "products"
	}
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), "products")
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return "products"
}

// ListProducts 返回所有合法产品名（含 profile.json 的子目录）。
func ListProducts() []string {
	entries, err := os.ReadDir(Dir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			if _, err := os.Stat(filepath.Join(Dir(), e.Name(), "profile.json")); err == nil {
				out = append(out, e.Name())
			}
		}
	}
	sort.Strings(out)
	return out
}

// LoadProfile 加载产品 profile；未指定时读环境变量，再回退默认产品。
// 未知产品返回 error（信息里列出可用产品）。
func LoadProfile(name string) (*Profile, error) {
	if name == "" {
		name = os.Getenv(EnvKey)
	}
	if name == "" {
		name = DefaultProduct
	}
	path := filepath.Join(Dir(), name, "profile.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &UnknownProductError{Name: name, Available: ListProducts()}
	}
	var raw struct {
		Title    string         `json:"title"`
		Features map[string]any `json:"features"`
		ExeName  string         `json:"exe_name"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	features := map[string]bool{}
	for k, v := range raw.Features {
		features[k] = truthy(v)
	}
	title := raw.Title
	if title == "" {
		title = name
	}
	return &Profile{Name: name, Title: title, Features: features, ExeName: raw.ExeName}, nil
}

func truthy(v any) bool {
	b, _ := v.(bool)
	return b
}

// UnknownProductError 未知产品。
type UnknownProductError struct {
	Name      string
	Available []string
}

func (e *UnknownProductError) Error() string { return "未知产品 " + e.Name }

// Active 当前进程的激活 profile（惰性加载一次）。
func Active() *Profile {
	mu.Lock()
	defer mu.Unlock()
	if active == nil {
		if p, err := LoadProfile(""); err == nil {
			active = p
		} else {
			active = &Profile{Name: DefaultProduct, Title: "Local AI Studio", Features: map[string]bool{}}
		}
	}
	return active
}

// Feature 查询当前产品的功能开关。
func Feature(key string, def bool) bool { return Active().Feature(key, def) }

// ResetForTest 清除缓存 profile（测试用）。
func ResetForTest() {
	mu.Lock()
	active = nil
	mu.Unlock()
}
