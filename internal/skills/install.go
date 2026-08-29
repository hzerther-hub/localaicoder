// 技能安装（对齐 Agent Skills 生态：Claude Code / OpenCode 等其它平台）：
// 从本地目录（git 浅克隆产物）或远程 Markdown URL 批量安装技能到用户级目录，
// 安装后立即参与 system prompt 注入。外部目录约定 skills/<name>/SKILL.md，
// 解析后扁平化为本工具的 <name>.md；同名技能跳过不覆盖（防破坏已有经验）。
package skills

import (
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MaxInstallPerOp 单次安装上限（防恶意仓库塞满技能目录）。
const MaxInstallPerOp = 50

// installHTTPClient 远程拉取客户端（15s 超时、1MB 上限由调用方 LimitReader 控制）。
var installHTTPClient = &http.Client{Timeout: 15 * time.Second}

// InstallFromDir 扫描目录（git 克隆产物或任意本地目录）并安装全部可识别技能：
//   - 任意层级的 SKILL.md / skill.md（Claude Code / OpenCode 目录约定）；
//   - 若一个都没找到，退回根目录 *.md（README 除外）中 frontmatter 完整者。
//
// 返回安装的技能名列表与因同名跳过的数量。dir 为空或不可读返回错误。
func InstallFromDir(dir string) (installed []string, skipped int, err error) {
	if dir == "" {
		return nil, 0, os.ErrInvalid
	}
	if _, err := os.ReadDir(dir); err != nil {
		return nil, 0, err
	}
	var files []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if name == "SKILL.md" || name == "skill.md" {
			files = append(files, p)
		}
		return nil
	})
	// 回退：仓库根目录的单文件技能（无任何 SKILL.md 时）
	if len(files) == 0 {
		root := dir
		if rootEntries, rerr := os.ReadDir(root); rerr == nil {
			for _, e := range rootEntries {
				n := e.Name()
				if !e.IsDir() && strings.HasSuffix(strings.ToLower(n), ".md") &&
					!strings.HasPrefix(strings.ToLower(n), "readme") {
					files = append(files, filepath.Join(root, n))
				}
			}
		}
	}
	existing := map[string]bool{}
	for _, sk := range LoadAll("") {
		existing[sk.Name] = true
	}
	for _, p := range files {
		if len(installed) >= MaxInstallPerOp {
			break
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			continue
		}
		sk, perr := parseSkillText(string(data))
		if perr != nil || strings.TrimSpace(sk.Body) == "" {
			continue // frontmatter 不完整或无正文：不是可安装技能
		}
		sk.Name = CleanName(sk.Name)
		if sk.Name == "" {
			sk.Name = CleanName(filepath.Base(filepath.Dir(p)))
		}
		if sk.Name == "" {
			continue
		}
		if existing[sk.Name] {
			skipped++
			continue
		}
		if _, serr := Save(sk, ScopeUser, ""); serr != nil {
			continue
		}
		existing[sk.Name] = true
		installed = append(installed, sk.Name)
	}
	return installed, skipped, nil
}

// InstallFromMarkdownURL 从远程 .md URL 安装单个技能（1MB 上限、15s 超时）。
// 返回安装的技能名；同名已存在或内容非法返回错误。
func InstallFromMarkdownURL(url string) (string, error) {
	resp, err := installHTTPClient.Get(url)
	if err != nil {
		return "", fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("读取失败: %w", err)
	}
	sk, err := parseSkillText(string(data))
	if err != nil {
		return "", fmt.Errorf("不是合法技能（缺 frontmatter）")
	}
	sk.Name = CleanName(sk.Name)
	if sk.Name == "" || strings.TrimSpace(sk.Body) == "" {
		return "", fmt.Errorf("缺少 name 或正文")
	}
	for _, ex := range LoadAll("") {
		if ex.Name == sk.Name {
			return "", fmt.Errorf("同名技能已存在：%s", sk.Name)
		}
	}
	if _, err := Save(sk, ScopeUser, ""); err != nil {
		return "", err
	}
	return sk.Name, nil
}
