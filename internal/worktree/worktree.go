package worktree

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	SprouteeDir = ".sproutee"
)

type Manager struct {
	RepoRoot string
}

func NewManager() (*Manager, error) {
	repoRoot, err := FindGitRepository()
	if err != nil {
		return nil, err
	}
	return &Manager{RepoRoot: repoRoot}, nil
}

func FindGitRepository() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get current directory: %w", err)
	}

	currentDir, err := filepath.Abs(wd)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path: %w", err)
	}

	for {
		gitDir := filepath.Join(currentDir, ".git")
		if stat, err := os.Stat(gitDir); err == nil {
			if stat.IsDir() {
				return currentDir, nil
			}

			data, err := os.ReadFile(gitDir)
			if err == nil && strings.HasPrefix(string(data), "gitdir: ") {
				return currentDir, nil
			}
		}

		parentDir := filepath.Dir(currentDir)
		if parentDir == currentDir {
			break
		}
		currentDir = parentDir
	}

	return "", fmt.Errorf("not inside a Git repository")
}

func generateTimestamp() string {
	return time.Now().Format("20060102_150405")
}

func (m *Manager) GenerateWorktreeDirName(name string) (string, error) {
	timestamp := generateTimestamp()
	return fmt.Sprintf("%s_%s", name, timestamp), nil
}

func (m *Manager) getProjectName() string {
	return filepath.Base(m.RepoRoot)
}

func (m *Manager) GetWorktreeBasePath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(m.RepoRoot, SprouteeDir)
	}

	projectName := m.getProjectName()
	return filepath.Join(homeDir, SprouteeDir, projectName)
}

func (m *Manager) branchExists(branch string) bool {
	cmd := exec.Command("git", "rev-parse", "--verify", branch)
	cmd.Dir = m.RepoRoot
	return cmd.Run() == nil
}

func (m *Manager) remoteBranchExists(branch string) bool {
	cmd := exec.Command("git", "rev-parse", "--verify", fmt.Sprintf("origin/%s", branch)) // #nosec G204
	cmd.Dir = m.RepoRoot
	return cmd.Run() == nil
}

func (m *Manager) fetchRemoteBranch(branch string) error {
	cmd := exec.Command("git", "fetch", "origin", fmt.Sprintf("%s:%s", branch, branch)) // #nosec G204
	cmd.Dir = m.RepoRoot

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to fetch remote branch: %w\nOutput: %s", err, string(output))
	}
	return nil
}

func (m *Manager) createNewBranch(branch string) error {
	cmd := exec.Command("git", "checkout", "-b", branch)
	cmd.Dir = m.RepoRoot

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create new branch: %w\nOutput: %s", err, string(output))
	}

	cmd = exec.Command("git", "checkout", "-")
	cmd.Dir = m.RepoRoot
	_ = cmd.Run()

	return nil
}

func (m *Manager) ensureBranchExists(branch string) error {
	if m.branchExists(branch) {
		return nil
	}

	if m.remoteBranchExists(branch) {
		return m.fetchRemoteBranch(branch)
	}

	return m.createNewBranch(branch)
}

func (m *Manager) CreateWorktree(name, branch string) (string, error) {
	if err := m.ensureBranchExists(branch); err != nil {
		return "", fmt.Errorf("failed to ensure branch exists: %w", err)
	}

	dirName, err := m.GenerateWorktreeDirName(name)
	if err != nil {
		return "", fmt.Errorf("failed to generate directory name: %w", err)
	}

	worktreeBasePath := m.GetWorktreeBasePath()
	if err := os.MkdirAll(worktreeBasePath, 0o755); err != nil {
		return "", fmt.Errorf("failed to create worktree base directory: %w", err)
	}

	worktreePath := filepath.Join(worktreeBasePath, dirName)

	cmd := exec.Command("git", "worktree", "add", worktreePath, branch)
	cmd.Dir = m.RepoRoot

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to create worktree: %w\nOutput: %s", err, string(output))
	}

	return worktreePath, nil
}

func (m *Manager) ListWorktrees() ([]Info, error) {
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir = m.RepoRoot

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to list worktrees: %w", err)
	}

	return parseWorktreeList(string(output))
}

type Info struct {
	Path   string
	Branch string
	Commit string
}

type Status struct {
	HasUnstagedChanges bool
	HasStagedChanges   bool
	HasUntrackedFiles  bool
	ChangedFiles       []string
	UntrackedFiles     []string
}

func parseWorktreeList(output string) ([]Info, error) {
	var worktrees []Info
	lines := strings.Split(strings.TrimSpace(output), "\n")

	var current Info
	for _, line := range lines {
		if line == "" {
			if current.Path != "" {
				worktrees = append(worktrees, current)
				current = Info{}
			}
			continue
		}

		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			continue
		}

		key, value := parts[0], parts[1]
		switch key {
		case "worktree":
			current.Path = value
		case "branch":
			current.Branch = strings.TrimPrefix(value, "refs/heads/")
		case "HEAD":
			current.Commit = value
		}
	}

	if current.Path != "" {
		worktrees = append(worktrees, current)
	}

	return worktrees, nil
}

func (m *Manager) CheckWorktreeStatus(worktreePath string) (*Status, error) {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = worktreePath

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to check git status: %w", err)
	}

	status := &Status{}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")

	for _, line := range lines {
		if len(line) < 2 {
			continue
		}

		indexStatus := line[0]
		workTreeStatus := line[1]
		fileName := strings.TrimSpace(line[2:])

		if indexStatus != ' ' && indexStatus != '?' {
			status.HasStagedChanges = true
			status.ChangedFiles = append(status.ChangedFiles, fileName)
		}

		if workTreeStatus != ' ' && workTreeStatus != '?' {
			status.HasUnstagedChanges = true
			if !contains(status.ChangedFiles, fileName) {
				status.ChangedFiles = append(status.ChangedFiles, fileName)
			}
		}

		if indexStatus == '?' && workTreeStatus == '?' {
			status.HasUntrackedFiles = true
			status.UntrackedFiles = append(status.UntrackedFiles, fileName)
		}
	}

	return status, nil
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func (s *Status) IsClean() bool {
	return !s.HasUnstagedChanges && !s.HasStagedChanges && !s.HasUntrackedFiles
}

func (s *Status) GetStatusSummary() string {
	if s.IsClean() {
		return "✅ Clean (no uncommitted changes)"
	}

	var issues []string
	if s.HasStagedChanges {
		issues = append(issues, "staged changes")
	}
	if s.HasUnstagedChanges {
		issues = append(issues, "unstaged changes")
	}
	if s.HasUntrackedFiles {
		issues = append(issues, fmt.Sprintf("%d untracked files", len(s.UntrackedFiles)))
	}

	return "⚠️  " + strings.Join(issues, ", ")
}

func (m *Manager) RemoveWorktree(worktreePath string) error {
	cmd := exec.Command("git", "worktree", "remove", worktreePath)
	cmd.Dir = m.RepoRoot

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to remove worktree: %w\nOutput: %s", err, string(output))
	}

	return nil
}

func (m *Manager) ForceRemoveWorktree(worktreePath string) error {
	cmd := exec.Command("git", "worktree", "remove", "--force", worktreePath)
	cmd.Dir = m.RepoRoot

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to force remove worktree: %w\nOutput: %s", err, string(output))
	}

	return nil
}
