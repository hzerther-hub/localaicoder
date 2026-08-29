// 文件改动历史：write_file 覆盖前快照旧内容，供「查看 diff / 一键还原」。
// 会话级内存状态（新会话由 ResetFileHistory 清空）。run_shell 的副作用不追踪
//（无法可靠快照，由 git 未提交改动兜底）。
package tools

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

type fileSnap struct {
	existed bool   // 写入前文件是否已存在（不存在 → 还原即删除）
	old     string // 写入前的原始内容
}

var (
	fhMu    sync.Mutex
	fhSnaps = map[string]fileSnap{}
)

// SnapshotBefore write_file 覆盖前调用：记录旧内容（仅首次，保留最早的原始态，
// 多次覆盖同一文件时仍可一键回到 AI 动手之前）。
func SnapshotBefore(path string) {
	data, err := os.ReadFile(path)
	fhMu.Lock()
	defer fhMu.Unlock()
	if _, exists := fhSnaps[path]; exists {
		return
	}
	fhSnaps[path] = fileSnap{existed: err == nil, old: string(data)}
}

// ChangedFiles 已快照（本会话 AI 写过）的文件列表，按名排序。
func ChangedFiles() []string {
	fhMu.Lock()
	defer fhMu.Unlock()
	out := make([]string, 0, len(fhSnaps))
	for p := range fhSnaps {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// FileDiff 单文件 diff（当前盘上内容 vs 快照旧内容，unified 格式）。
// 超长文件退化为统计摘要（LCS 内存防爆炸）。
func FileDiff(path string) string {
	fhMu.Lock()
	snap, ok := fhSnaps[path]
	fhMu.Unlock()
	if !ok {
		return "错误：该文件没有本会话的改动记录"
	}
	cur, err := os.ReadFile(path)
	if err != nil {
		return "错误：读取当前文件失败：" + err.Error()
	}
	if !snap.existed {
		lines := strings.Split(string(cur), "\n")
		if len(lines) > 400 {
			return fmt.Sprintf("新文件 %s（%d 行）——内容过长，仅显示统计；可用编辑器或 git 查看全文", path, len(lines))
		}
		var b strings.Builder
		fmt.Fprintf(&b, "--- /dev/null（新文件）\n+++ %s\n", path)
		for _, l := range lines {
			b.WriteString("+" + l + "\n")
		}
		return b.String()
	}
	return unifiedDiff(strings.Split(snap.old, "\n"), strings.Split(string(cur), "\n"), path)
}

// RevertFile 还原到 write_file 之前的原始内容；快照前不存在的文件则删除。
func RevertFile(path string) error {
	fhMu.Lock()
	snap, ok := fhSnaps[path]
	fhMu.Unlock()
	if !ok {
		return os.ErrNotExist
	}
	var err error
	if snap.existed {
		err = os.WriteFile(path, []byte(snap.old), 0o644)
	} else {
		err = os.Remove(path)
	}
	if err == nil {
		fhMu.Lock()
		delete(fhSnaps, path)
		fhMu.Unlock()
	}
	return err
}

// ResetFileHistory 清空快照（新会话时调用）。
func ResetFileHistory() {
	fhMu.Lock()
	fhSnaps = map[string]fileSnap{}
	fhMu.Unlock()
}

// ---------------- 简化 unified diff（LCS 行级对齐，3 行上下文） ----------------

const diffCellCap = 4_000_000 // LCS DP 单元上限（约 2000×2000），超出退化为摘要

// unifiedDiff 对新旧行序列做 LCS 对齐，输出 unified 格式（变更 ±3 行上下文，
// 相邻变更合并为同一 hunk）。供界面展示，非严格 git 补丁。
func unifiedDiff(a, b []string, path string) string {
	if len(a)*len(b) > diffCellCap {
		return fmt.Sprintf("%s：变更过大（%d → %d 行），仅显示统计；可用 git diff 查看全文",
			path, len(a), len(b))
	}
	ops := lcsOps(a, b) // 0 保留 / 1 删 a 行 / 2 增 b 行
	var changed []int
	for k, o := range ops {
		if o != 0 {
			changed = append(changed, k)
		}
	}
	if len(changed) == 0 {
		return path + "：无差异"
	}
	const ctxN = 3
	groups := [][]int{{changed[0]}}
	for _, c := range changed[1:] {
		if c-groups[len(groups)-1][len(groups[len(groups)-1])-1] <= 2*ctxN {
			groups[len(groups)-1] = append(groups[len(groups)-1], c)
		} else {
			groups = append(groups, []int{c})
		}
	}

	posAt := func(idx int) (ai, bj int) {
		for k := 0; k < idx; k++ {
			switch ops[k] {
			case 1:
				ai++
			case 2:
				bj++
			default:
				ai++
				bj++
			}
		}
		return
	}

	var out strings.Builder
	fmt.Fprintf(&out, "--- a/%s（AI 改动前）\n+++ b/%s（当前）\n", path, path)
	for _, g := range groups {
		lo := max(0, g[0]-ctxN)
		hi := min(len(ops), g[len(g)-1]+1+ctxN)
		ai, bj := posAt(lo)
		fmt.Fprintf(&out, "@@ -%d +%d @@\n", ai+1, bj+1)
		for k := lo; k < hi; k++ {
			switch ops[k] {
			case 1:
				fmt.Fprintf(&out, "-%s\n", a[ai])
				ai++
			case 2:
				fmt.Fprintf(&out, "+%s\n", b[bj])
				bj++
			default:
				fmt.Fprintf(&out, " %s\n", a[ai])
				ai++
				bj++
			}
		}
	}
	return out.String()
}

// lcsOps 返回对齐操作序列（0 保留 / 1 删 a[i] / 2 增 b[j]）。
func lcsOps(a, b []string) []int {
	n, m := len(a), len(b)
	dp := make([]int32, (n+1)*(m+1))
	at := func(x, y int) int32 { return dp[x*(m+1)+y] }
	for x := n - 1; x >= 0; x-- {
		for y := m - 1; y >= 0; y-- {
			if a[x] == b[y] {
				dp[x*(m+1)+y] = at(x+1, y+1) + 1
			} else if at(x+1, y) >= at(x, y+1) {
				dp[x*(m+1)+y] = at(x+1, y)
			} else {
				dp[x*(m+1)+y] = at(x, y+1)
			}
		}
	}
	var out []int
	x, y := 0, 0
	for x < n && y < m {
		switch {
		case a[x] == b[y]:
			out = append(out, 0)
			x++
			y++
		case at(x+1, y) >= at(x, y+1):
			out = append(out, 1)
			x++
		default:
			out = append(out, 2)
			y++
		}
	}
	for ; x < n; x++ {
		out = append(out, 1)
	}
	for ; y < m; y++ {
		out = append(out, 2)
	}
	return out
}
